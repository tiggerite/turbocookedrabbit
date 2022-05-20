package tcr

import (
	"crypto/tls"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ConnectionHost is an internal representation of amqp.Connection.
type ConnectionHost struct {
	Connection         *amqp.Connection
	ConnectionID       uint64
	CachedChannelCount uint64
	uri                string
	connectionName     string
	heartbeatInterval  time.Duration
	connectionTimeout  time.Duration
	tlsConfig          *TLSConfig
	Errors             chan *amqp.Error
	Blockers           chan amqp.Blocking
	connLock           *sync.Mutex
}

// NewConnectionHost creates a simple ConnectionHost wrapper for management by end-user developer.
func NewConnectionHost(
	uri string,
	connectionName string,
	connectionID uint64,
	heartbeatInterval time.Duration,
	connectionTimeout time.Duration,
	tlsConfig *TLSConfig) (*ConnectionHost, error) {

	connHost := &ConnectionHost{
		uri:               uri,
		connectionName:    connectionName,
		ConnectionID:      connectionID,
		heartbeatInterval: heartbeatInterval,
		connectionTimeout: connectionTimeout,
		tlsConfig:         tlsConfig,
		Errors:            make(chan *amqp.Error, 10),
		Blockers:          make(chan amqp.Blocking, 10),
		connLock:          &sync.Mutex{},
	}

	err := connHost.Connect()
	if err != nil {
		return nil, err
	}

	return connHost, nil
}

// Connect tries to connect (or reconnect) to the provided properties of the host one time.
func (ch *ConnectionHost) Connect() error {

	// Compare, Lock, Recompare Strategy
	if ch.Connection != nil && !ch.Connection.IsClosed() /* <- atomic */ {
		return nil
	}

	ch.connLock.Lock() // Block all but one.
	defer ch.connLock.Unlock()

	// Recompare, check if an operation is still necessary after acquiring lock.
	if ch.Connection != nil && !ch.Connection.IsClosed() /* <- atomic */ {
		return nil
	}

	// Proceed with reconnectivity
	var amqpConn *amqp.Connection
	var actualTLSConfig *tls.Config
	var err error

	if ch.tlsConfig != nil && ch.tlsConfig.EnableTLS {

		actualTLSConfig, err = CreateTLSConfig(
			ch.tlsConfig.PEMCertLocation,
			ch.tlsConfig.LocalCertLocation)
		if err != nil {
			return err
		}
	}

	if actualTLSConfig == nil {
		amqpConn, err = amqp.DialConfig(ch.uri, amqp.Config{
			Heartbeat: ch.heartbeatInterval,
			Dial:      amqp.DefaultDial(ch.connectionTimeout),
			Properties: amqp.Table{
				"connection_name": ch.connectionName,
			},
		})
	} else {
		amqpConn, err = amqp.DialConfig("amqps://"+ch.tlsConfig.CertServerName, amqp.Config{
			Heartbeat:       ch.heartbeatInterval,
			Dial:            amqp.DefaultDial(ch.connectionTimeout),
			TLSClientConfig: actualTLSConfig,
			Properties: amqp.Table{
				"connection_name": ch.connectionName,
			},
		})
	}
	if err != nil {
		return err
	}

	ch.Connection = amqpConn
	ch.Errors = make(chan *amqp.Error, 10)
	ch.Blockers = make(chan amqp.Blocking, 10)

	ch.Connection.NotifyClose(ch.Errors) // ch.Errors is closed by streadway/amqp in some scenarios :(
	ch.Connection.NotifyBlocked(ch.Blockers)

	return nil
}

// PauseOnFlowControl allows you to wait and sleep while receiving flow control messages.
// Sleeps for one second, repeatedly until the blocking has stopped.
func (ch *ConnectionHost) PauseOnFlowControl() {

	ch.connLock.Lock()
	defer ch.connLock.Unlock()

	for {
		// nothing we can do (race condition) Blockers
		// and will deadlock if it is read from.
		if ch.Connection.IsClosed( /* atomic */ ) {
			return
		}

		select {
		case blocker := <-ch.Blockers: // Check for flow control issues.
			if !blocker.Active {
				return
			}
			time.Sleep(time.Second)
		default:
			return
		}
	}
}
