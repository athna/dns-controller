# Ingress to DNS Record Service

This service sits in the control plane and monitors ingress creation, deletion and modification 
to post updates to a DNS server about the clusters ingressControllers and 
the host headers that they are aware of how to route.

This service requires a certain amount of RBAC inside of the cluster established via service principal
Below is an outline of the rules

## RBAC

Since this service reaches accross namespaces for much of the functions it performs, and requires access to list all of the clusters nodes, ClusterRole and ClusterRoleBinding are used in placed of role and rolebinding.

Below is an outline of the RBAC policies this service requires in order to run, and reasons why.

```
- apiGroups:
  - ""
  resources:
  - namespaces
  - nodes
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - extensions
  resources:
  - ingresses
  verbs:
  - get
  - list
  - watch
  ```

  ### Core API group ""

  The service needs to be able to get,list,and watch nodes and namespaces.  When updating a single host entry contained in an ingress the list method shall be used with a listoption label selector to filture out nodes which run load balancers specificallly.  Currently these nodes are labeled `net=loadbalancer`.  Short of any tolerations deployments like logging frameworks might specify these are the only pods allowed to schedule to these nodes.  The watch method should be used to monitor load balancer scaling and create additional A records for each host entry.

  ### Extensions API group "extensions"

  This service needs to be able to watch ingresses in all namespaces and update DNS entries to point to existing load balancers.  In the even of load balancer scaling the list option can be used to grab each ingress resources host header.

## Operation

This service currently expects inCluster authentication for access to the kubernetes client.  Watches are created to recieve events from the control plane.  Upon reception of an event the appropriate DNS resources are created, updated, or deleted.  Currently this is done by RESTful messaging using gRPC method calls to an in house DNS.  A RESTful implementation using HTTP/JSON would work here and support additional DNS providers.

### References 
    Traefik Ingress Controller
    https://docs.traefik.io/user-guide/kubernetes/

#### Referenced Libraries
	bitbucket.ema.emoneyadvisor.com/cp/auth
	bitbucket.ema.emoneyadvisor.com/cp/dns/lib/go/v1
	google.golang.org/grpc
	k8s.io/api/core/v1
    k8s.io/api/extensions/v1beta1
	k8s.io/apimachinery/pkg/apis/meta/v1
	k8s.io/apimachinery/pkg/watch
	k8s.io/client-go/kubernetes