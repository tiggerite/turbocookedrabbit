package main_test

import (
	"testing"
	"time"

	"github.com/fortytw2/leaktest"
	"github.com/stretchr/testify/assert"
	"github.com/tiggerite/turbocookedrabbit/v2/pkg/tcr"
)

func TestCreateRabbitService(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	service, err := tcr.NewRabbitService(Seasoning, "", "", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, service)

	service.Shutdown(true)
}

func TestCreateRabbitServiceWithEncryption(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	Seasoning.EncryptionConfig.Enabled = true
	service, err := tcr.NewRabbitService(Seasoning, "PasswordyPassword", "SaltySalt", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, service)

	service.Shutdown(true)
}

func TestRabbitServicePublish(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	Seasoning.EncryptionConfig.Enabled = true
	service, err := tcr.NewRabbitService(Seasoning, "PasswordyPassword", "SaltySalt", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, service)

	data := tcr.RandomBytes(1000)
	_ = service.Publish(data, "", "TcrTestQueue", "", false, nil)

	service.Shutdown(true)
}

func TestRabbitServicePublishLetter(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	Seasoning.EncryptionConfig.Enabled = true
	service, err := tcr.NewRabbitService(Seasoning, "PasswordyPassword", "SaltySalt", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, service)

	letter := tcr.CreateMockRandomLetter("TcrTestQueue")
	_ = service.PublishLetter(letter)

	service.Shutdown(true)
}

func TestRabbitServicePublishAndConsumeLetter(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	Seasoning.EncryptionConfig.Enabled = true
	service, err := tcr.NewRabbitService(Seasoning, "PasswordyPassword", "SaltySalt", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, service)

	letter := tcr.CreateMockRandomLetter("TcrTestQueue")
	_ = service.PublishLetter(letter)

	service.Shutdown(true)
}

func TestRabbitServicePublishLetterToNonExistentQueueForRetryTesting(t *testing.T) {
	defer leaktest.Check(t)() // Fail on leaked goroutines.

	Seasoning.PublisherConfig.PublishTimeOutInterval = 0 // triggering instant timeouts for retry test
	service, err := tcr.NewRabbitService(Seasoning, "PasswordyPassword", "SaltySalt", nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, service)

	letter := tcr.CreateMockRandomLetter("QueueDoesNotExist")
	_ = service.QueueLetter(letter)

	timeout := time.After(time.Duration(2 * time.Second))
	<-timeout

	service.Shutdown(true)
}
