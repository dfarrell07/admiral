package broker

import (
	"fmt"

	"github.com/submariner-io/admiral/pkg/federate"
	"github.com/submariner-io/admiral/pkg/log"
	"github.com/submariner-io/admiral/pkg/util"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog"
)

const (
	metadataField = "metadata"
	labelsField   = "labels"
)

type federator struct {
	dynClient       dynamic.Interface
	restMapper      meta.RESTMapper
	brokerNamespace string
	localClusterID  string
}

var keepMetadataFields = map[string]bool{"name": true, "namespace": true, labelsField: true, "annotations": true}

func NewFederator(localClusterID string) (federate.Federator, error) {
	restConfig, namespace, err := buildBrokerConfig()
	if err != nil {
		return nil, err
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating dynamic client: %v", err)
	}

	restMapper, err := util.BuildRestMapper(restConfig)
	if err != nil {
		return nil, err
	}

	return &federator{
		dynClient:       dynClient,
		restMapper:      restMapper,
		brokerNamespace: namespace,
		localClusterID:  localClusterID,
	}, nil
}

// Use for testing purposes only
func NewTestFederator(dynClient dynamic.Interface, restMapper meta.RESTMapper, brokerNamespace string, localClusterID string) federate.Federator {
	return &federator{
		dynClient:       dynClient,
		restMapper:      restMapper,
		brokerNamespace: brokerNamespace,
		localClusterID:  localClusterID,
	}
}

func (f *federator) Distribute(resource runtime.Object) error {
	klog.V(log.DEBUG).Infof("In Distribute for %#v", resource)

	toDistribute, resourceClient, err := f.toUnstructured(resource)
	if err != nil {
		return err
	}

	if f.localClusterID != "" {
		setNestedField(toDistribute.Object, f.localClusterID, metadataField, labelsField, federate.ClusterIDLabelKey)
	}

	f.prepareResourceForSync(toDistribute)

	_, err = util.CreateOrUpdate(resourceClient, toDistribute, func(existing *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		// Preserve the existing metadata info (except Labels and Annotations), specifically the ResourceVersion which must
		// be set on an update operation.
		existing.SetLabels(toDistribute.GetLabels())
		existing.SetAnnotations(toDistribute.GetAnnotations())
		setNestedField(toDistribute.Object, getMetadata(existing), metadataField)
		return toDistribute, nil
	})

	return err
}

func (f *federator) Delete(resource runtime.Object) error {
	toDelete, resourceClient, err := f.toUnstructured(resource)
	if err != nil {
		return err
	}

	klog.V(log.DEBUG).Infof("Deleting resource: %#v", toDelete)

	return resourceClient.Delete(toDelete.GetName(), &metav1.DeleteOptions{})
}

func (f *federator) toUnstructured(from runtime.Object) (*unstructured.Unstructured, dynamic.ResourceInterface, error) {
	to, gvr, err := util.ToUnstructuredResource(from, f.restMapper)
	if err != nil {
		return nil, nil, err
	}

	return to, f.dynClient.Resource(*gvr).Namespace(f.brokerNamespace), nil
}

func (f *federator) prepareResourceForSync(resource *unstructured.Unstructured) {
	//  Remove metadata fields that are set by the API server on creation.
	metadata := getMetadata(resource)
	for field := range metadata {
		if !keepMetadataFields[field] {
			unstructured.RemoveNestedField(resource.Object, metadataField, field)
		}
	}

	resource.SetNamespace(f.brokerNamespace)
}

func getMetadata(from *unstructured.Unstructured) map[string]interface{} {
	value, _, _ := unstructured.NestedFieldNoCopy(from.Object, metadataField)
	if value != nil {
		return value.(map[string]interface{})
	}

	return map[string]interface{}{}
}

func setNestedField(to map[string]interface{}, value interface{}, fields ...string) {
	if value != nil {
		err := unstructured.SetNestedField(to, value, fields...)
		if err != nil {
			klog.Errorf("Error setting value (%v) for nested field %v in object %v: %v", value, fields, to, err)
		}
	}
}
