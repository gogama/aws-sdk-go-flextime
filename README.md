flextime - Adaptive timeouts for AWS SDK for Go
===============================================

[![Build Status](https://travis-ci.com/gogama/aws-sdk-go-flextime.svg)](https://travis-ci.com/gogama/aws-sdk-go-flextime) [![Go Report Card](https://goreportcard.com/badge/github.com/gogama/aws-sdk-go-flextime)](https://goreportcard.com/report/github.com/gogama/aws-sdk-go-flextime) [![PkgGoDev](https://pkg.go.dev/badge/github.com/gogama/aws-sdk-go-flextime)](https://pkg.go.dev/github.com/gogama/aws-sdk-go-flextime)

Package flextime provides adaptive timeouts when retrying requests to AWS
services using the [AWS SDK for Go](https://github.com/aws/aws-sdk-go).

Getting Started
===============

Install flextime:

```sh
$ go get github.com/gogama/aws-sdk-go-flextime
```

Import the flextime package and install a TimeoutFunc on your AWS SDK service
client begin enjoy adaptive timeouts when calling that service:

```go
package main

import (
	"time"
	
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/gogama/aws-sdk-go-flextime/flextime"
)

func main() {
	s := session.Must(session.NewSession())
	// Create a DynamoDB client
	ddb := dynamodb.New(s)
	// Set an initial request timeout on the DynamoDB client at 200ms, a first
	// retry timeout at 800ms, and all subsequent retry timeouts at 2 seconds.
	flextime.OnClient(ddb.Client, flextime.Sequence(200*time.Millisecond, 800*time.Millisecond, 2*time.Second))
}
```

If you have a more general timeout strategy, you can install it on the session,
so it will be used by all AWS service clients created from that session.

```go
package main

import (
	"time"

	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/locationservice"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/gogama/aws-sdk-go-flextime/flextime"
)

func timeoutStrategy(r *request.Request, n int) time.Duration {
	// Your custom timeout strategy implementation.
}

func main() {
	s := session.Must(session.NewSession())
	flextime.OnSession(s, timeoutStrategy)
	locClient := locationservice.New(s) // Amazon Location Service client uses custom timeout strategy.
	sqsClient := sqs.New(s)             // Simple Queue Service client uses same custom timeout strategy.
	doStuff(locClient, sqsClient)
}
```

Check out [the full API documentation](https://pkg.go.dev/github.com/gogama/aws-sdk-go-flextime).

---

License
=======

This project is licensed under the terms of the MIT License.

---

FAQ
===

## What Go versions are supported?

Package flextime works on Go 1.13 and higher.

## What AWS SDK for Go versions are supported?

Package flextime works on AWS SDK 1.14.0 and higher.

*The new [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) is not
supported.*

## Why do I want adaptive timeouts on the AWS SDK for Go?

AWS services are normally pretty fast, but sometimes they do experience "pockets
of badness" where some requests end up taking a long time. Often, simply
cancelling the request and retrying it can result in a faster service time on
the second attempt, giving a better overall experience for your customers
downstream.

However, a problem arises when the AWS service is experiencing a prolonged period
of  degraded performance. If you have set a low static timeout, you may experience
the worst possible scenario:

- Your initial request times out.
- All your retries time out.
- Your pointless retries are contributing to overloading the AWS service, and
  you may be paying for those cancelled requests despite not getting any service. 
- *Your* service is taking forever to respond to *its* downstream clients, who
  are themselves by now starting to time out and retry, exponentially growing
  the retry stormfront!

Adaptive timeouts help you balance the competing concerns by starting with a low
defensive timeout to maintain your service performance during a "pocket of
badness" from the upstream AWS service while transitioning to a higher "wait
patiently" mode if the low timeout strategy isn't producing results.

## How does flextime play with the other HTTP client timeouts?

Excellent question. The AWS SDK for Go relies on the Go standard library HTTP
client (`http.Client` from package net/http), and you can configure the standard
client within your AWS SDK client. The standard client supports multiple timeouts:

- The field `Client.Timeout` allows global static timeout applicable to all
  requests made from the client. The default zero value indicates no timeout.
- The `http.Transport` structure allows global static timeouts to be set on
  granular components of the HTTP transaction (dial, TLS handshake, expect
  continue, response headers). Again the default zero value indicates no timeout.

If you configure the above static timeouts with non-zero values, they will
conflict with flextime's timeouts, and you may get unexpected results.
Therefore, it is preferable to leave them with the zero value.

## How does flextime play with context deadlines?

When using the AWS SDK for Go's "WithContext" methods, you can provide a
`context.Context` when making AWS SDK requests, and the SDK will respect the
context deadline, if one is set. Such a deadline applies to the entire "logical"
SDK request (including all retries), so it is not at the individual HTTP request
level, and it is not adaptable.

An AWS SDK client with flextime installed will continue to respect the context
deadline as normal. This means you can have an adaptive timeout policy applying
at the individual HTTP request level *and* a macro timeout applying to the
entire "logical" request.
