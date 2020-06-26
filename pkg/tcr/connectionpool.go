package tcr

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Workiva/go-datastructures/queue"
)

// ConnectionPool houses the pool of RabbitMQ connections.
type ConnectionPool struct {
	config               PoolConfig
	connectionName       string
	uri                  string
	heartbeatInterval    time.Duration
	connectionTimeout    time.Duration
	connections          *queue.Queue
	channels             chan *ChannelHost
	connectionID         uint64
	poolLock             *sync.Mutex
	poolRWLock           *sync.RWMutex
	connectionLock       int32
	flaggedConnections   map[uint64]bool
	sleepOnErrorInterval time.Duration
}

// NewConnectionPool creates hosting structure for the ConnectionPool.
func NewConnectionPool(config *PoolConfig) (*ConnectionPool, error) {

	if config.ConnectionPoolConfig.Heartbeat == 0 || config.ConnectionPoolConfig.ConnectionTimeout == 0 {
		return nil, errors.New("connectionpool heartbeat or connectiontimeout can't be 0")
	}

	if config.ConnectionPoolConfig.MaxConnectionCount == 0 {
		return nil, errors.New("connectionpool maxconnectioncount can't be 0")
	}

	cp := &ConnectionPool{
		config:               *config,
		uri:                  config.ConnectionPoolConfig.URI,
		connectionName:       config.ConnectionPoolConfig.ConnectionName,
		heartbeatInterval:    time.Duration(config.ConnectionPoolConfig.Heartbeat) * time.Second,
		connectionTimeout:    time.Duration(config.ConnectionPoolConfig.ConnectionTimeout) * time.Second,
		connections:          queue.New(int64(config.ConnectionPoolConfig.MaxConnectionCount)), // possible overflow error
		channels:             make(chan *ChannelHost, config.ConnectionPoolConfig.MaxCacheChannelCount),
		poolLock:             &sync.Mutex{},
		poolRWLock:           &sync.RWMutex{},
		flaggedConnections:   make(map[uint64]bool),
		sleepOnErrorInterval: time.Duration(config.ConnectionPoolConfig.SleepOnErrorInterval) * time.Millisecond,
	}

	if ok := cp.initializeConnections(); !ok {
		return nil, errors.New("initialization failed during connection creation")
	}

	return cp, nil
}

func (cp *ConnectionPool) initializeConnections() bool {

	cp.connectionID = 0
	cp.connections = queue.New(int64(cp.config.ConnectionPoolConfig.MaxConnectionCount))

	for i := uint64(0); i < cp.config.ConnectionPoolConfig.MaxConnectionCount; i++ {

		connectionHost, err := NewConnectionHost(
			cp.uri,
			cp.connectionName+"-"+strconv.FormatUint(cp.connectionID, 10),
			cp.connectionID,
			cp.heartbeatInterval,
			cp.connectionTimeout,
			cp.config.ConnectionPoolConfig.TLSConfig)

		if err != nil {
			return false
		}

		if err = cp.connections.Put(connectionHost); err != nil {
			return false
		}

		cp.connectionID++
	}

	for i := uint64(0); i < cp.config.ConnectionPoolConfig.MaxConnectionCount; i++ {
		cp.channels <- cp.CreateChannel(i, true)
	}

	return true
}

// GetConnection gets a connection based on whats in the ConnectionPool (blocking under bad network conditions).
// Flowcontrol (blocking) or transient network outages will pause here until cleared.
// Uses the SleepOnErrorInterval to pause between retries.
func (cp *ConnectionPool) GetConnection() (*ConnectionHost, error) {

	var connectionHost *ConnectionHost
GetConnectionHost:
	for {

		// Pull from the queue.
		// Pauses here if the queue is empty.
		structs, err := cp.connections.Get(1)
		if err != nil {
			return nil, err
		}

		var ok bool
		connectionHost, ok = structs[0].(*ConnectionHost)
		if !ok {
			return nil, errors.New("invalid struct type found in ConnectionPool queue")
		}

		select {
		case <-connectionHost.Blockers(): // Check for flow control issues.
			if cp.sleepOnErrorInterval > 0 {
				time.Sleep(cp.sleepOnErrorInterval)
			}
			cp.ReturnConnection(connectionHost, false)
		default:
			break GetConnectionHost
		}
	}

	healthy := true
	select {
	case <-connectionHost.Errors():
		healthy = false
	default:
		break
	}

	// Makes debugging easier
	connectionClosed := connectionHost.Connection.IsClosed()
	connectionFlagged := cp.IsConnectionFlagged(connectionHost.ConnectionID)

	// Between these three states we do our best to determine that a connection is dead in the various lifecycles.
	if connectionFlagged || !healthy || connectionClosed {

		cp.FlagConnection(connectionHost.ConnectionID)

		var err error
		replacementConnectionID := connectionHost.ConnectionID
		connectionHost = nil

		// Do not leave without a good Connection.
		for connectionHost == nil {

			connectionHost, err = NewConnectionHost(
				cp.uri,
				cp.connectionName+"-"+strconv.FormatUint(replacementConnectionID, 10),
				replacementConnectionID,
				cp.heartbeatInterval,
				cp.connectionTimeout,
				cp.config.ConnectionPoolConfig.TLSConfig)
			if err != nil {

				if cp.sleepOnErrorInterval > 0 {
					time.Sleep(cp.sleepOnErrorInterval)
				}

				continue
			}
		}

		cp.UnflagConnection(replacementConnectionID)
	}

	return connectionHost, nil
}

// ReturnConnection puts the connection back in the queue.
// This helps maintain a Round Robin on Connections and their resources.
func (cp *ConnectionPool) ReturnConnection(connHost *ConnectionHost, flag bool) {

	if flag {
		cp.FlagConnection(connHost.ConnectionID)
	}

	cp.connections.Put(connHost)
}

// GetChannel gets a cached ackable channel from the Pool if they exist or creates a channel.
// A non-acked channel is always a transient channel.
// Blocking if Ackable is true and the cache is empty.
// If you want a transient Ackable channel (un-managed), use CreateChannel directly.
func (cp *ConnectionPool) GetChannel(ackable bool) *ChannelHost {
	if ackable && cp.config.ConnectionPoolConfig.MaxCacheChannelCount > 0 {
		return <-cp.channels
	}

	return cp.CreateChannel(10000, ackable)
}

// ReturnChannel returns a cached Channel. If erred, new Channel is created instead and then returned to the cache.
func (cp *ConnectionPool) ReturnChannel(channelHost *ChannelHost, erred bool) {

	// If called by user with the wrong channel don't add a non-managed channel back to the channel cache.
	if channelHost.ID < cp.config.ConnectionPoolConfig.MaxCacheChannelCount {
		if erred {
			var currentID = channelHost.ID
			var ackable = channelHost.Ackable
			channelHost = cp.CreateChannel(currentID, ackable)
			cp.channels <- channelHost
		}

		cp.channels <- channelHost
		return
	}

	channelHost.Close()
}

// CreateChannel allows you create a ChannelHost which helps wrap Amqp Channel functionality.
func (cp *ConnectionPool) CreateChannel(id uint64, ackable bool) *ChannelHost {

	// prevents assigning to reserved ChannelId for the Cache of Channels
	if id < cp.config.ConnectionPoolConfig.MaxCacheChannelCount {
		id = 10000
	}

	for {
		connHost, err := cp.GetConnection()
		if err != nil {
			if cp.sleepOnErrorInterval > 0 {
				time.Sleep(cp.sleepOnErrorInterval)
			}
			continue
		}

		chanHost, err := NewChannelHost(connHost.Connection, id, connHost.ConnectionID, ackable)
		if err != nil {
			if cp.sleepOnErrorInterval > 0 {
				time.Sleep(cp.sleepOnErrorInterval)
			}
			cp.ReturnConnection(connHost, true)
			continue
		}

		cp.ReturnConnection(connHost, false)
		return chanHost
	}
}

// ConnectionCount flags that connection as usable in the future. Careful, locking call.
func (cp *ConnectionPool) ConnectionCount() int64 {
	return cp.connections.Len() // Locking
}

// UnflagConnection flags that connection as usable in the future.
func (cp *ConnectionPool) UnflagConnection(connectionID uint64) {
	cp.poolRWLock.Lock()
	defer cp.poolRWLock.Unlock()
	cp.flaggedConnections[connectionID] = false
}

// FlagConnection flags that connection as non-usable in the future.
func (cp *ConnectionPool) FlagConnection(connectionID uint64) {
	cp.poolRWLock.Lock()
	defer cp.poolRWLock.Unlock()
	cp.flaggedConnections[connectionID] = true
}

// IsConnectionFlagged checks to see if the connection has been flagged for removal.
func (cp *ConnectionPool) IsConnectionFlagged(connectionID uint64) bool {
	cp.poolRWLock.RLock()
	defer cp.poolRWLock.RUnlock()
	if flagged, ok := cp.flaggedConnections[connectionID]; ok {
		return flagged
	}

	return false
}

// Shutdown closes all connections in the ConnectionPool and resets the Pool to pre-initialized state.
func (cp *ConnectionPool) Shutdown() {
	cp.poolLock.Lock()
	defer cp.poolLock.Unlock()

	// Create connection lock (> 0)
	atomic.AddInt32(&cp.connectionLock, 1)

	for !cp.connections.Empty() {
		items, _ := cp.connections.Get(cp.connections.Len())

		for _, item := range items {
			connectionHost := item.(*ConnectionHost)
			if !connectionHost.Connection.IsClosed() {
				connectionHost.Connection.Close()
			}
		}
	}

	cp.connections = queue.New(int64(cp.config.ConnectionPoolConfig.MaxConnectionCount))
	cp.flaggedConnections = make(map[uint64]bool)
	cp.connectionID = 0

	// Release connection lock (0)
	atomic.StoreInt32(&cp.connectionLock, 0)
}