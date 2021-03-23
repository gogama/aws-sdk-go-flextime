// Copyright 2021 The flextime Authors. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
Package flextime provides adaptive timeouts when retrying AWS SDK
requests.

Out of the box, the AWS SDK for Go only supports the static timeout
values available on the Go standard HTTP client (http.Client from
package net/http). These timeout values cannot be changed per request
attempt (initial attempt and retry). Install a flextime timeout
function on an AWS SDK session or client to vary timeouts across request
attempts.

To set an initial low timeout, and back off to successively higher
timeout values, use a sequence:

	f := flextime.Sequence(350*time.Millisecond, 1*time.Second, 2*time.Second)
	s := session.Must(session.NewSession())
	err := flextime.OnSession(s, f)   // Install timeout sequence all clients with session
	if err != nil {
		// Handle error
	}
	ddb := dynamodb.New(s)            // New DynamoDB client with session, will use f
	loc := locationservice.New(s)     // New Amazon Location Service client with session, will use f

To add install a timeout function on a specific client instance:

	c := sqs.New(s)
	err := flextime.OnClient(c.Client, f) // Install timeout function f on new SQS client
	if err != nil {
		// Handle error
	}

To roll your own timeout function:

	func myTimeoutFunc(r *request.Request, int n) time.Duration {
		return ...
	}

	func main() {
		var err error
		err = flextime.OnSession(s, myTimeoutFunc)         // Install for all clients with session...
		if err != nil {
			// Handle error
		}
		err = flextime.OnClient(ddb.Client, myTimeoutFunc) // ...or install for specific client only
		if err != nil {
			// Handle error
		}
	}
*/
package flextime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"

	"github.com/aws/aws-sdk-go/aws/corehandlers"

	"github.com/aws/aws-sdk-go/aws"

	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
)

// A TimeoutFunc computes a timeout for an AWS SDK request attempt based
// on the request state and the number n of timeouts that have occurred
// while executing previous attempts.
//
// A positive return value sets a timeout of that duration on the next
// request attempt. A zero return value means no timeout.
type TimeoutFunc func(r *request.Request, n int) time.Duration

// OnSession sets the TimeoutFunc used to compute timeouts for all
// clients created using the given AWS SDK session. The previous
// TimeoutFunc on the session, if any, is replaced.
func OnSession(s *session.Session, f TimeoutFunc) error {
	if s == nil {
		panic("flextime: nil session")
	}
	return onHandlerList(s.Handlers.Send, f)
}

// OnClient sets the TimeoutFunc used to compute timeouts for the given
// AWS SDK client. The previous TimeoutFunc on the session, if any, is
// replaced.
func OnClient(c *client.Client, f TimeoutFunc) error {
	if c == nil {
		panic("flextime: nil client")
	}
	return onHandlerList(c.Handlers.Send, f)
}

// Sequence constructs a timeout function that varies the next timeout
// value if the previous attempt timed out.
//
// Use Sequence if you find the remote service often exhibits one-off
// slow response times that can be cured by quickly timing out and
// retrying, but you also need to protect your application (and the
// remote service) from retry storms and failure if the remote service
// goes through a burst of slowness where most response times during the
// burst are slower than your usual quick timeout.
//
// Parameter usual represents the timeout value the function will return
// for an initial attempt and for any retry where the immediately
// preceding attempt did not time out.
//
// Parameter after contains timeout values the function will return if
// the previous attempt timed out. If this was the first timeout of the
// execution, after[0] is returned; if the second, after[1], and so on.
// If more attempts have timed out within the client request than after
// has elements, then the last element of after is returned.
//
// Consider the following timeout function:
//
// 	f := Sequence(200*time.Millisecond, time.Second, 10*time.Second)
//
// The function f will use 200 milliseconds as the usual timeout but if
// the preceding attempt timed out and was the first timeout of the
// client request, it will use 1 second; and if the previous attempt
// timed out and was not the first attempt, it will use 10 seconds.
func Sequence(usual time.Duration, after ...time.Duration) TimeoutFunc {
	p := make([]time.Duration, 1, 1+len(after))
	p[0] = usual
	p = append(p, after...)

	return func(_ *request.Request, n int) time.Duration {
		i := n
		if i > len(p)-1 {
			i = len(p) - 1
		}
		return p[i]
	}
}

type configKeyType string

const configKey configKeyType = "flextime.ConfigKey"

type config struct {
	n int // Number of consecutive timeouts
}

const (
	handlerName       = "flextime.SendHandler"
	nilTimeoutFuncMsg = "flextime: nil timeout func"
	nilWrappedFuncMsg = "flextime: nil wrapped func"
	failedInstallMsg  = "flextime: failed swap send handler"
)

var coreSendHandler = corehandlers.SendHandler

func onHandlerList(hl request.HandlerList, f TimeoutFunc) error {
	h := request.NamedHandler{
		Name: handlerName,
		Fn:   wrapWithTimeout(f, coreSendHandler.Fn),
	}
	if !hl.Swap(coreSendHandler.Name, h) && !hl.Swap(handlerName, h) {
		return errors.New(failedInstallMsg)
	}
	return nil
}

func wrapWithTimeout(tf TimeoutFunc, wf func(*request.Request)) func(*request.Request) {
	if tf == nil {
		panic(nilTimeoutFuncMsg)
	}
	if wf == nil {
		panic(nilWrappedFuncMsg)
	}
	return func(r *request.Request) {
		awsCtx := r.Context()
		prevHTTPReq := r.HTTPRequest
		prevHTTPCtx := prevHTTPReq.Context()
		val := awsCtx.Value(configKey)
		cfg, ok := val.(*config)
		if !ok {
			cfg = &config{}
			awsCtx = context.WithValue(awsCtx, configKey, cfg)
			// Update the context on the AWS request. This will also replace the
			// context on the HTTP request, which can break some integrations,
			// such as X-Ray, which only add values into the HTTP request
			// context. To fix this, we will work with/put back the HTTP context
			// below.
			r.SetContext(awsCtx)
		}
		timeout := tf(r, cfg.n)
		logDebug(r, "timeout %v", timeoutFmt(timeout))
		if timeout > 0 {
			// Create HTTP request context, with timeout, based on previous HTTP
			// request context, so we don't overwrite values put into the HTTP
			// context by other integrations such as X-Ray.
			httpCtx, _ := context.WithTimeout(prevHTTPCtx, timeout)
			tempReq := prevHTTPReq.WithContext(httpCtx)
			defer func() { r.HTTPRequest = prevHTTPReq }()
			r.HTTPRequest = tempReq
		} else if !ok {
			// Put back old HTTP request context.
			r.HTTPRequest = r.HTTPRequest.WithContext(prevHTTPCtx)
		}
		wf(r)
		if isTimeout(r.Error) {
			cfg.n++
		}
	}
}

func timeoutFmt(timeout time.Duration) interface{} {
	if timeout > 0 {
		return timeout
	}
	return "OFF"
}

func logDebug(r *request.Request, format string, a ...interface{}) {
	if r.Config.Logger != nil && r.Config.LogLevel.AtLeast(aws.LogDebug) {
		format = "DEBUG: flextime %s/%s " + format
		a = append([]interface{}{r.ClientInfo.ServiceName, r.Operation.Name}, a...)
		msg := fmt.Sprintf(format, a...)
		r.Config.Logger.Log(msg)
	}
}

func isTimeout(err error) bool {
	var awsErr awserr.Error
	if errors.As(err, &awsErr) {
		err = awsErr.OrigErr()
	}
	ot, ok := err.(interface {
		Timeout() bool
	})
	return ok && ot.Timeout()
}
