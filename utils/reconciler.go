package utils

import (
	"context"
	"fmt"
	"time"

	appstacksv1beta2 "github.com/application-stacks/runtime-component-operator/api/v1beta2"
	"github.com/application-stacks/runtime-component-operator/common"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ReconcileInterval = 15
)

// ReconcilerBase base reconciler with some common behaviour
type ReconcilerBase struct {
	apiReader  client.Reader
	client     client.Client
	scheme     *runtime.Scheme
	recorder   record.EventRecorder
	restConfig *rest.Config
	discovery  discovery.DiscoveryInterface
	controller controller.Controller
}

//NewReconcilerBase creates a new ReconcilerBase
func NewReconcilerBase(apiReader client.Reader, client client.Client, scheme *runtime.Scheme, restConfig *rest.Config, recorder record.EventRecorder) ReconcilerBase {
	return ReconcilerBase{
		apiReader:  apiReader,
		client:     client,
		scheme:     scheme,
		recorder:   recorder,
		restConfig: restConfig,
	}
}

// GetController returns controller
func (r *ReconcilerBase) GetController() controller.Controller {
	return r.controller
}

// SetController sets controller
func (r *ReconcilerBase) SetController(c controller.Controller) {
	r.controller = c
}

// GetClient returns client
func (r *ReconcilerBase) GetClient() client.Client {
	return r.client
}

// GetAPIReader returns a client.Reader. Use client.Reader only if a
// particular resource does not implement the 'watch' verb such as
// ImageStreamTag. This is because the operator-sdk Client
// automatically performs a Watch on all the objects that are obtained
// with Get, but some resources such as the ImageStreamTag kind does not
// implement the Watch verb, which caused errors.
// Here is an example of how the error would look like:
//  `Failed to watch *v1.ImageStreamTag: the server does not allow this method on the requested resource (get imagestreamtags.image.openshift.io)`
func (r *ReconcilerBase) GetAPIReader() client.Reader {
	return r.apiReader
}

// GetRecorder returns the underlying recorder
func (r *ReconcilerBase) GetRecorder() record.EventRecorder {
	return r.recorder
}

// GetDiscoveryClient ...
func (r *ReconcilerBase) GetDiscoveryClient() (discovery.DiscoveryInterface, error) {
	if r.discovery == nil {
		var err error
		r.discovery, err = discovery.NewDiscoveryClientForConfig(r.restConfig)
		return r.discovery, err
	}

	return r.discovery, nil
}

// SetDiscoveryClient ...
func (r *ReconcilerBase) SetDiscoveryClient(discovery discovery.DiscoveryInterface) {
	r.discovery = discovery
}

var log = logf.Log.WithName("utils")

// CreateOrUpdate ...
func (r *ReconcilerBase) CreateOrUpdate(obj client.Object, owner metav1.Object, reconcile func() error) error {

	if owner != nil {
		controllerutil.SetControllerReference(owner, obj, r.scheme)
	}

	result, err := controllerutil.CreateOrUpdate(context.TODO(), r.GetClient(), obj, reconcile)
	if err != nil {
		return err
	}

	var gvk schema.GroupVersionKind
	gvk, err = apiutil.GVKForObject(obj, r.scheme)
	if err == nil {
		log.Info("Reconciled", "Kind", gvk.Kind, "Namespace", obj.GetNamespace(), "Name", obj.GetName(), "Status", result)
	}

	return err
}

// DeleteResource deletes kubernetes resource
func (r *ReconcilerBase) DeleteResource(obj client.Object) error {
	err := r.client.Delete(context.TODO(), obj)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Unable to delete object ", "object", obj)
			return err
		}
		return nil
	}

	metaObj, ok := obj.(metav1.Object)
	if !ok {
		err := fmt.Errorf("%T is not a metav1.Object", obj)
		log.Error(err, "Failed to convert into metav1.Object")
		return err
	}

	var gvk schema.GroupVersionKind
	gvk, err = apiutil.GVKForObject(obj, r.scheme)
	if err == nil {
		log.Info("Reconciled", "Kind", gvk.Kind, "Name", metaObj.GetName(), "Status", "deleted")
	}
	return nil
}

// DeleteResources ...
func (r *ReconcilerBase) DeleteResources(resources []client.Object) error {
	for i := range resources {
		err := r.DeleteResource(resources[i])
		if err != nil {
			return err
		}
	}
	return nil
}

// GetOpConfigMap ...
func (r *ReconcilerBase) GetOpConfigMap(name string, ns string) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	err := r.GetClient().Get(context.TODO(), types.NamespacedName{Name: name, Namespace: ns}, configMap)
	if err != nil {
		return nil, err
	}
	return configMap, nil
}

// ManageError ...
func (r *ReconcilerBase) ManageError(issue error, conditionType common.StatusConditionType, ba common.BaseComponent) (reconcile.Result, error) {
	s := ba.GetStatus()
	obj := ba.(client.Object)
	logger := log.WithValues("ba.Namespace", obj.GetNamespace(), "ba.Name", obj.GetName())
	logger.Error(issue, "ManageError", "Condition", conditionType, "ba", ba)
	r.GetRecorder().Event(obj, "Warning", "ProcessingError", issue.Error())

	oldCondition := s.GetCondition(conditionType)
	if oldCondition == nil {
		oldCondition = &appstacksv1beta2.StatusCondition{}
	}

	lastStatus := oldCondition.GetStatus()

	// Keep the old `LastTransitionTime` when status has not changed
	nowTime := metav1.Now()
	transitionTime := oldCondition.GetLastTransitionTime()
	if lastStatus == corev1.ConditionTrue {
		transitionTime = &nowTime
	}

	newCondition := s.NewCondition()
	newCondition.SetLastTransitionTime(transitionTime)
	newCondition.SetReason(string(apierrors.ReasonForError(issue)))
	newCondition.SetType(conditionType)
	newCondition.SetMessage(issue.Error())
	newCondition.SetStatus(corev1.ConditionFalse)

	s.SetCondition(newCondition)

	err := r.UpdateStatus(obj)
	if err != nil {

		if apierrors.IsConflict(issue) {
			return reconcile.Result{Requeue: true}, nil
		}
		logger.Error(err, "Unable to update status")
		return reconcile.Result{
			RequeueAfter: time.Second,
			Requeue:      true,
		}, nil
	}

	// StatusReasonInvalid means the requested create or update operation cannot be
	// completed due to invalid data provided as part of the request. Don't retry.
	// if apierrors.IsInvalid(issue) {
	// 	return reconcile.Result{}, nil
	// }

	var retryInterval time.Duration
	if lastStatus == corev1.ConditionTrue {
		retryInterval = time.Second
	} else {
		retryInterval = 5 * time.Second
	}

	return reconcile.Result{
		//RequeueAfter: time.Duration(math.Min(float64(retryInterval.Nanoseconds()*2), float64(time.Hour.Nanoseconds()*6))),
		RequeueAfter: retryInterval,
		Requeue:      true,
	}, nil
}

// ManageSuccess ...
func (r *ReconcilerBase) ManageSuccess(conditionType common.StatusConditionType, ba common.BaseComponent) (reconcile.Result, error) {
	s := ba.GetStatus()
	oldCondition := s.GetCondition(conditionType)
	if oldCondition == nil {
		oldCondition = &appstacksv1beta2.StatusCondition{}
	}

	// Keep the old `LastTransitionTime` when status has not changed
	nowTime := metav1.Now()
	transitionTime := oldCondition.GetLastTransitionTime()
	if oldCondition.GetStatus() == corev1.ConditionFalse {
		transitionTime = &nowTime
	}

	statusCondition := s.NewCondition()
	statusCondition.SetLastTransitionTime(transitionTime)
	statusCondition.SetReason("")
	statusCondition.SetMessage("")
	statusCondition.SetStatus(corev1.ConditionTrue)
	statusCondition.SetType(conditionType)

	s.SetCondition(statusCondition)
	err := r.UpdateStatus(ba.(client.Object))
	if err != nil {
		log.Error(err, "Unable to update status")
		return reconcile.Result{
			RequeueAfter: time.Second,
			Requeue:      true,
		}, nil
	}
	return reconcile.Result{RequeueAfter: ReconcileInterval * time.Second}, nil
}

// IsGroupVersionSupported ...
func (r *ReconcilerBase) IsGroupVersionSupported(groupVersion string, kind string) (bool, error) {
	cli, err := r.GetDiscoveryClient()
	if err != nil {
		log.Error(err, "Failed to return a discovery client for the current reconciler")
		return false, err
	}

	res, err := cli.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	for _, v := range res.APIResources {
		if v.Kind == kind {
			return true, nil
		}
	}

	return false, nil
}

// UpdateStatus updates the fields corresponding to the status subresource for the object
func (r *ReconcilerBase) UpdateStatus(obj client.Object) error {
	return r.GetClient().Status().Update(context.Background(), obj)
}

// IsOpenShift returns true if the operator is running on an OpenShift platform
func (r *ReconcilerBase) IsOpenShift() bool {
	isOpenShift, err := r.IsGroupVersionSupported(routev1.SchemeGroupVersion.String(), "Route")
	if err != nil {
		return false
	}
	return isOpenShift
}

// GetRouteTLSValues returns certificate an key values to be used in the route
func (r *ReconcilerBase) GetRouteTLSValues(ba common.BaseComponent) (key string, cert string, ca string, destCa string, err error) {
	key, cert, ca, destCa = "", "", "", ""
	mObj := ba.(metav1.Object)
	if ba.GetService() != nil && ba.GetService().GetCertificateSecretRef() != nil {
		tlsSecret := &corev1.Secret{}
		secretName := mObj.GetName() + "-svc-tls"
		if ba.GetService().GetCertificateSecretRef() != nil {
			secretName = *ba.GetService().GetCertificateSecretRef()
		}
		err := r.GetClient().Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: mObj.GetNamespace()}, tlsSecret)
		if err != nil {
			r.ManageError(err, common.StatusConditionTypeReconciled, ba)
			return "", "", "", "", err
		}
		caCrt, ok := tlsSecret.Data["ca.crt"]
		if ok {
			destCa = string(caCrt)
		}
	}
	if ba.GetRoute() != nil && ba.GetRoute().GetCertificateSecretRef() != nil {
		tlsSecret := &corev1.Secret{}
		secretName := mObj.GetName() + "-route-tls"
		if ba.GetRoute().GetCertificateSecretRef() != nil {
			secretName = *ba.GetRoute().GetCertificateSecretRef()
		}
		err := r.GetClient().Get(context.TODO(), types.NamespacedName{Name: secretName, Namespace: mObj.GetNamespace()}, tlsSecret)
		if err != nil {
			r.ManageError(err, common.StatusConditionTypeReconciled, ba)
			return "", "", "", "", err
		}
		v, ok := tlsSecret.Data["ca.crt"]
		if ok {
			ca = string(v)
		}
		v, ok = tlsSecret.Data["tls.crt"]
		if ok {
			cert = string(v)
		}
		v, ok = tlsSecret.Data["tls.key"]
		if ok {
			key = string(v)
		}
		v, ok = tlsSecret.Data["destCA.crt"]
		if ok {
			destCa = string(v)
		}
	}
	return key, cert, ca, destCa, nil
}
