package tcr

import (
	"errors"

	"github.com/streadway/amqp"
)

// ChannelHost is an internal representation of amqp.Connection.
type ChannelHost struct {
	Channel       *amqp.Channel
	ID            uint64
	ConnectionID  uint64
	Ackable       bool
	ErrorMessages chan *ErrorMessage
	Confirmations chan amqp.Confirmation
	errors        chan *amqp.Error
}

// NewChannelHost creates a simple ConnectionHost wrapper for management by end-user developer.
func NewChannelHost(
	amqpConn *amqp.Connection,
	id uint64,
	connectionID uint64,
	ackable bool) (*ChannelHost, error) {

	if amqpConn.IsClosed() {
		return nil, errors.New("can't open a channel - connection is already closed")
	}

	amqpChan, err := amqpConn.Channel()
	if err != nil {
		return nil, err
	}

	channelHost := &ChannelHost{
		Channel:      amqpChan,
		ConnectionID: connectionID,
		Ackable:      ackable,
		errors:       make(chan *amqp.Error, 1),
	}

	channelHost.Channel.NotifyClose(channelHost.errors)

	if ackable {
		if err = channelHost.Channel.Confirm(false); err != nil {
			return nil, err
		}
	}

	return channelHost, nil
}

// Close allows for manual close of Amqp Channel kept internally.
func (ch *ChannelHost) Close() {
	ch.Channel.Close()
}

// Errors allow you to listen for amqp.Error messages.
func (ch *ChannelHost) Errors() <-chan *ErrorMessage {
	select {
	case amqpError := <-ch.errors:
		if amqpError != nil { // received a nil during testing
			ch.ErrorMessages <- NewErrorMessage(amqpError)
		}
	default:
		break
	}

	return ch.ErrorMessages
}