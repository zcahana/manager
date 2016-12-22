// Copyright 2016 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kube

import (
	"log"
	"sync"
	"time"

	"k8s.io/client-go/pkg/util/flowcontrol"
)

// Queue of work tickets processed using a rate-limiting loop
type Queue interface {
	// Push a ticket
	Push(Task)
	// Run the loop until a signal on the channel
	Run(chan struct{})
}

// Handler specifies a function to apply on an object for a given event type
type Handler func(obj interface{}, event int) error

// Task object for the event watchers; processes until handler succeeds
type Task struct {
	handler Handler
	obj     interface{}
	event   int
}

type queueImpl struct {
	delay   time.Duration
	queue   []Task
	lock    sync.Mutex
	closing bool
}

// NewQueue instantiates a queue with a processing function
func NewQueue(errorDelay time.Duration) Queue {
	return &queueImpl{
		delay:   errorDelay,
		queue:   make([]Task, 0),
		closing: false,
		lock:    sync.Mutex{},
	}
}

func (q *queueImpl) Push(item Task) {
	q.lock.Lock()
	if !q.closing {
		q.queue = append(q.queue, item)
	}
	q.lock.Unlock()
}

func (q *queueImpl) Run(stop chan struct{}) {
	go func() {
		<-stop
		q.lock.Lock()
		q.closing = true
		q.lock.Unlock()
	}()

	// Throttle processing up to smoothed 10 qps with bursts up to 100 qps
	rateLimiter := flowcontrol.NewTokenBucketRateLimiter(float32(10), 100)
	var item Task
	for {
		rateLimiter.Accept()

		q.lock.Lock()
		if q.closing {
			q.lock.Unlock()
			return
		} else if len(q.queue) == 0 {
			q.lock.Unlock()
		} else {
			item, q.queue = q.queue[0], q.queue[1:]
			q.lock.Unlock()

			for {
				err := item.handler(item.obj, item.event)
				if err != nil {
					log.Printf("Work item failed (%v), repeating after delay %v", err, q.delay)
					time.Sleep(q.delay)
				} else {
					break
				}
			}
		}
	}
}

// chainHandler applies handlers in a sequence
type chainHandler struct {
	funcs []Handler
}

func (ch *chainHandler) apply(obj interface{}, event int) error {
	for _, f := range ch.funcs {
		if err := f(obj, event); err != nil {
			return err
		}
	}
	return nil
}

func (ch *chainHandler) append(h Handler) {
	ch.funcs = append(ch.funcs, h)
}