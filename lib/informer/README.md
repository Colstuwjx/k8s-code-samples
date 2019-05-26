# informer

informer is part of the [client-go](https://github.com/kubernetes/client-go) library, it implements k8s resources watch mechanism, which is one of the most important building block of kubernetes.

This demo illustrates the usage of informer, including POD (the others are TODO) etc, it starts resources informer, and print logs while received informer event by adding event handler, while user typed `Ctrl+C` or sent `SIGTERM`, it will do gracefully exit.
