// Copyright 2021 The flextime Authors. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
Package flextime provides adaptive timeouts when retrying AWS SDK v2
requests.

Install a flextime timeout function on an AWS SDK v2 config to vary
timeouts across request attempts.

To set an initial low timeout, and back off to successively higher
timeout values, use a sequence:

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		// Handle error
	}

	f := flextime.Sequence(350*time.Millisecond, 1*time.Second, 2*time.Second)
	flextime.OnConfig(&cfg, f)
	client := dynamodb.NewFromConfig(cfg)

To roll your own timeout function:

	func myTimeoutFunc(attempt int) time.Duration {
		return time.Duration(attempt) * 500 * time.Millisecond
	}

	flextime.OnConfig(&cfg, myTimeoutFunc)
	client := s3.NewFromConfig(cfg)
*/
package flextime

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go/middleware"
)

// A TimeoutFunc computes a timeout for an AWS SDK v2 request attempt based
// on the attempt number (starting from 0 for the initial attempt).
//
// A positive return value sets a timeout of that duration on the next
// request attempt. A zero return value means no timeout.
type TimeoutFunc func(attempt int) time.Duration

// OnConfig configures the given AWS SDK v2 config to use the
// provided TimeoutFunc for adaptive timeouts.
func OnConfig(cfg *aws.Config, f TimeoutFunc) {
	if f == nil {
		panic("flextime: nil timeout func")
	}

	// Create a shared attempt counter
	attemptCounter := &attemptCounter{}

	// Add our middleware to the config's APIOptions
	middlewareFunc := func(stack *middleware.Stack) error {
		return stack.Deserialize.Add(&timeoutMiddleware{
			timeoutFunc: f,
			counter:     attemptCounter,
		}, middleware.Before)
	}

	cfg.APIOptions = append(cfg.APIOptions, middlewareFunc)
}

type attemptCounter struct {
	count int
}

type timeoutMiddleware struct {
	timeoutFunc TimeoutFunc
	counter     *attemptCounter
}

func (m *timeoutMiddleware) ID() string {
	return "FlextimeTimeout"
}

func (m *timeoutMiddleware) HandleDeserialize(
	ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler,
) (middleware.DeserializeOutput, middleware.Metadata, error) {
	// Get current attempt number
	attempt := m.counter.count
	m.counter.count++

	// Calculate timeout
	timeout := m.timeoutFunc(attempt)

	if timeout > 0 {
		// Create new context with timeout
		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = timeoutCtx
	}

	output, metadata, err := next.HandleDeserialize(ctx, in)

	// Debug: log what happened
	if err != nil {
		println("Middleware attempt", attempt, "failed with:", err.Error())
	}

	return output, metadata, err
}

// Sequence constructs a timeout function that varies the next timeout
// value based on the attempt number.
//
// Parameter usual represents the timeout value for the initial attempt.
// Parameter after contains timeout values for subsequent attempts.
// If more attempts are made than after has elements, the last element
// of after is returned.
func Sequence(usual time.Duration, after ...time.Duration) TimeoutFunc {
	timeouts := make([]time.Duration, 1, 1+len(after))
	timeouts[0] = usual
	timeouts = append(timeouts, after...)

	return func(attempt int) time.Duration {
		if attempt >= len(timeouts) {
			return timeouts[len(timeouts)-1]
		}
		return timeouts[attempt]
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.Error timeout
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// Check for context deadline exceeded
	contextExceeded := errors.Is(err, context.DeadlineExceeded)
	return contextExceeded
}
