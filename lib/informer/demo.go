package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

type Informer struct {
	Pod cache.SharedIndexInformer
}

func (i *Informer) Run(stopCh chan struct{}) {
	go i.Pod.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh,
		i.Pod.HasSynced,
	) {
		err := fmt.Errorf("Timed out waiting for caches to sync")
		runtime.HandleError(err)
	}
}

type PodLister struct {
	cache.Store
}

type Lister struct {
	Pod PodLister
}

type K8sStore struct {
	informers *Informer
	listers   *Lister
}

func NewK8sStore(
	namespace string, resyncPeriod time.Duration,
	client clientset.Interface,
) *K8sStore {
	store := &K8sStore{
		informers: &Informer{},
		listers:   &Lister{},
	}

	// create informers factory, enable and assign required informers
	infFactory := informers.NewSharedInformerFactoryWithOptions(client, resyncPeriod,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(*metav1.ListOptions) {}))
	store.informers.Pod = infFactory.Core().V1().Pods().Informer()
	store.listers.Pod.Store = store.informers.Pod.GetStore()

	podEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// "k8s.io/apimachinery/pkg/apis/meta/v1" provides an Object
			// interface that allows us to get metadata easily
			mObj := obj.(metav1.Object)
			klog.Infof("Added pod: %s", mObj.GetName())
		},
		UpdateFunc: func(old, cur interface{}) {
			oldPod := old.(*corev1.Pod)
			curPod := cur.(*corev1.Pod)

			if oldPod.Status.Phase == curPod.Status.Phase {
				return
			}

			klog.Infof("Updated pod: %v, old: %v", curPod, oldPod)
		},
		DeleteFunc: func(obj interface{}) {
			klog.Infof("Deleted pod: %v", obj)
		},
	}
	store.informers.Pod.AddEventHandler(podEventHandler)
	return store
}

func (s *K8sStore) Run(stopCh chan struct{}) {
	s.informers.Run(stopCh)
}

type DummyController struct {
	stopCh chan struct{}
	store  *K8sStore
}

func NewDummyController(client clientset.Interface) *DummyController {
	store := NewK8sStore(metav1.NamespaceAll, 0, client)
	return &DummyController{
		stopCh: make(chan struct{}),
		store:  store,
	}
}

func (dc *DummyController) Start() {
	dc.store.Run(dc.stopCh)

	for {
		select {
		case <-dc.stopCh:
			break
		}
	}
}

func (dc *DummyController) Stop() error {
	close(dc.stopCh)
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
		klog.Errorf("Error during shutdown: %v", err)
		exitCode = 1
	}

	time.Sleep(1 * time.Second)

	klog.Infof("Exiting with %v", exitCode)
	exit(exitCode)
}

func main() {
	var (
		kubeconfig string
	)

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.Parse()

	kubeconfigFromENV := os.Getenv("KUBECONFIG")
	if kubeconfigFromENV != "" {
		kubeconfig = kubeconfigFromENV
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	client, err := clientset.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	klog.InitFlags(nil)

	c := NewDummyController(client)
	go handleSigterm(c, func(code int) {
		os.Exit(code)
	})
	c.Start()
}
