package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

type Informer struct {
	Pod      cache.SharedIndexInformer
	Ingress  cache.SharedIndexInformer
	Endpoint cache.SharedIndexInformer
}

func (i *Informer) Run(stopCh chan struct{}) {
	go i.Pod.Run(stopCh)
	go i.Endpoint.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh,
		i.Pod.HasSynced,
	) {
		err := fmt.Errorf("Timed out waiting for caches to sync")
		runtime.HandleError(err)
	}

	// in big clusters, deltas can keep arriving even after HasSynced
	// functions have returned 'true'
	time.Sleep(1 * time.Second)

	// we can start syncing ingress objects only after other caches are
	// ready, because ingress rules require content from other listers, and
	// 'add' events get triggered in the handlers during caches population.
	go i.Ingress.Run(stopCh)
	if !cache.WaitForCacheSync(stopCh,
		i.Ingress.HasSynced,
	) {
		err := fmt.Errorf("Timed out waiting for caches to sync")
		runtime.HandleError(err)
	}
}

type PodLister struct {
	cache.Store
}

type IngressLister struct {
	cache.Store
}

type EndpointLister struct {
	cache.Store
}

type Lister struct {
	Pod      PodLister
	Ingress  IngressLister
	Endpoint EndpointLister
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

	store.informers.Ingress = infFactory.Extensions().V1beta1().Ingresses().Informer()
	store.listers.Ingress.Store = store.informers.Ingress.GetStore()

	store.informers.Endpoint = infFactory.Core().V1().Endpoints().Informer()
	store.listers.Endpoint.Store = store.informers.Endpoint.GetStore()

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

	ingEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ing := obj.(*extensions.Ingress)
			klog.Infof("Added ingress: %v", ing)
		},

		DeleteFunc: func(obj interface{}) {
			ing, ok := obj.(*extensions.Ingress)
			if !ok {
				// If we reached here it means the ingress was deleted but its final state is unrecorded.
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					klog.Errorf("couldn't get object from tombstone %#v", obj)
					return
				}
				ing, ok = tombstone.Obj.(*extensions.Ingress)
				if !ok {
					klog.Errorf("Tombstone contained object that is not an Ingress: %#v", obj)
					return
				}
			}

			klog.Infof("Deleted ingress: %v", ing)
		},

		UpdateFunc: func(old, cur interface{}) {
			oldIng := old.(*extensions.Ingress)
			curIng := cur.(*extensions.Ingress)
			klog.Infof("Updated ingress: %v, old: %v", curIng, oldIng)
		},
	}
	store.informers.Ingress.AddEventHandler(ingEventHandler)

	epEventHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ep := obj.(*corev1.Endpoints)
			klog.Infof("Added endpoint: %v", ep)
		},
		DeleteFunc: func(obj interface{}) {
			klog.Infof("Deleted endpoint: %v", obj)
		},
		UpdateFunc: func(old, cur interface{}) {
			oep := old.(*corev1.Endpoints)
			cep := cur.(*corev1.Endpoints)
			if !reflect.DeepEqual(cep.Subsets, oep.Subsets) {
				klog.Infof("Updated endpoint: %v, old: %v", cep, oep)
			}
		},
	}
	store.informers.Endpoint.AddEventHandler(epEventHandler)

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
