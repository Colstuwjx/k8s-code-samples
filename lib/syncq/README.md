# syncq

[kubernetes ingress-nginx controller](https://github.com/kubernetes/ingress-nginx) implemented a [task queue](https://github.com/kubernetes/ingress-nginx/blob/master/internal/task/queue.go) to collaboration the change event and do `syncIngress` task.

This demo illustrates the usage of task queue with a super simple dummy controller, it starts sync queue, and fires `initial` task, while user typed `Ctrl+C` or sent `SIGTERM`, it will do gracefully exit.
