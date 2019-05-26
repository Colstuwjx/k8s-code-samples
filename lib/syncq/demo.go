package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/klog"
)

type DummyController struct {
	syncQueue *Queue
	stopCh    chan struct{}

	// stopLock is used to enforce that only a single call to Stop send at
	// a given time. We allow stopping through an HTTP endpoint and
	// allowing concurrent stoppers leads to stack traces.
	stopLock *sync.Mutex

	isShuttingDown bool
}

func NewDummyController() *DummyController {
	dc := &DummyController{
		stopCh:   make(chan struct{}),
		stopLock: &sync.Mutex{},
	}
	dc.syncQueue = NewTaskQueue(dc.doFakedJob)
	return dc
}

func (dc *DummyController) Start() {
	go dc.syncQueue.Run(time.Second, dc.stopCh)
	dc.syncQueue.EnqueueTask(GetDummyObject("initial"))

	for {
		select {
		case <-dc.stopCh:
			break
		}
	}
}

func (dc *DummyController) Stop() error {
	dc.isShuttingDown = true

	dc.stopLock.Lock()
	defer dc.stopLock.Unlock()

	if dc.syncQueue.IsShuttingDown() {
		return fmt.Errorf("shutdown already in progress")
	}

	close(dc.stopCh)
	go dc.syncQueue.Shutdown()

	return nil
}

func (dc *DummyController) doFakedJob(obj interface{}) error {
	if dc.syncQueue.IsShuttingDown() {
		return nil
	}

	klog.Infof("Received event %v", obj)
	return nil
}

type exiter func(code int)

func handleSigterm(c *DummyController, exit exiter) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	<-signalChan
	klog.Info("Received SIGTERM/Interrupt, shutting down")

	exitCode := 0
	if err := c.Stop(); err != nil {
		klog.Infof("Error during shutdown: %v", err)
		exitCode = 1
	}

	time.Sleep(1 * time.Second)

	klog.Infof("Exiting with %v", exitCode)
	exit(exitCode)
}

func main() {
	klog.InitFlags(nil)

	c := NewDummyController()
	go handleSigterm(c, func(code int) {
		os.Exit(code)
	})
	c.Start()
}
