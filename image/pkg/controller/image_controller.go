package controller

import (
	"fmt"
	"time"

	perceptorapi "github.com/blackducksoftware/perceptor/pkg/api"

	"github.com/blackducksoftware/perceivers/image/pkg/mapper"
	"github.com/blackducksoftware/perceivers/pkg/communicator"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"

	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	imageapi "github.com/openshift/api/image/v1"
	imageclient "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	imagelister "github.com/openshift/client-go/image/listers/image/v1"

	log "github.com/sirupsen/logrus"
)

// ImageController handles watching images and sending them to perceptor
type ImageController struct {
	client          *imageclient.ImageV1Client
	imageController cache.Controller
	indexer         cache.Indexer
	imageLister     imagelister.ImageLister
	imageURL        string

	syncHandler func(key string) error
	queue       workqueue.RateLimitingInterface
}

// NewImageController creates a new ImageController object
func NewImageController(oic *imageclient.ImageV1Client, perceptorURL string) *ImageController {
	ic := ImageController{
		client:   oic,
		queue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "Images"),
		imageURL: fmt.Sprintf("%s/%s", perceptorURL, perceptorapi.ImagePath),
	}
	ic.indexer, ic.imageController = cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
				return ic.client.Images().List(opts)
			},
			WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
				return ic.client.Images().Watch(opts)
			},
		},
		&imageapi.Image{},
		0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: ic.enqueueJob,
			UpdateFunc: func(oldObj, newObj interface{}) {
				old, ok1 := oldObj.(*imageapi.Image)
				new, ok2 := newObj.(*imageapi.Image)
				if ok1 && ok2 && ic.needsUpdate(old, new) {
					ic.enqueueJob(newObj)
				}
			},
			DeleteFunc: ic.enqueueJob,
		},
		cache.Indexers{},
	)
	ic.imageLister = imagelister.NewImageLister(ic.indexer)
	ic.syncHandler = ic.processImage

	return &ic
}

// Run starts a controller that watches images and sends them to perceptor
func (ic *ImageController) Run(threadiness int, stopCh <-chan struct{}) {
	defer ic.queue.ShutDown()

	go ic.imageController.Run(stopCh)

	// Start up your worker threads based on threadiness.  Some controllers have multiple kinds of workers
	for i := 0; i < threadiness; i++ {
		// runWorker will loop until "something bad" happens.  The .Until will then rekick the worker
		// after one second
		go wait.Until(ic.runWorker, time.Second, stopCh)
	}

	// Wait until we're told to stop
	<-stopCh
}

func (ic *ImageController) enqueueJob(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err == nil {
		ic.queue.Add(key)
	}
}

func (ic *ImageController) needsUpdate(oldObj *imageapi.Image, newObj *imageapi.Image) bool {
	return true
}

func (ic *ImageController) runWorker() {
	// Hot loop until we're told to stop.  processNextWorkItem will automatically wait until there's work
	// available, so we don't worry about secondary waits
	for ic.processNextWorkItem() {
	}
}

// processNextWorkItem deals with one key off the queue.  It returns false when it's time to quit.
func (ic *ImageController) processNextWorkItem() bool {
	// Pull the next work item from queue.  It should be a key we use to lookup something in a cache
	keyObj, quit := ic.queue.Get()
	if quit {
		return false
	}
	// You always have to indicate to the queue that you've completed a piece of work
	defer ic.queue.Done(keyObj)

	key := keyObj.(string)
	// Do your work on the key.  This method will contains your "do stuff" logic
	err := ic.syncHandler(key)
	if err == nil {
		// if you had no error, tell the queue to stop tracking history for your key.  This will
		// reset things like failure counts for per-item rate limiting
		ic.queue.Forget(key)
		return true
	}

	// There was a failure so be sure to report it.  This method allows for pluggable error handling
	// which can be used for things like cluster-monitoring
	utilruntime.HandleError(fmt.Errorf("%v failed with : %v", key, err))

	// Since we failed, we should requeue the item to work on later.  This method will add a backoff
	// to avoid hotlooping on particular items (they're probably still not going to work right away)
	// and overall controller protection (everything I've done is broken, this controller needs to
	// calm down or it can starve other useful work) cases.
	ic.queue.AddRateLimited(key)

	return true
}

func (ic *ImageController) processImage(key string) error {
	log.Infof("processing image %s", key)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return fmt.Errorf("error getting name of image %q to get image from informer: %v", key, err)
	}

	// Get the image
	image, err := ic.imageLister.Get(name)
	if errors.IsNotFound(err) {
		// Image doesn't exist (anymore), so this is a delete event
		return communicator.SendPerceptorDeleteEvent(ic.imageURL, name)
	} else if err != nil {
		return fmt.Errorf("error getting image %s from informer: %v", name, err)
	}

	// Convert the image from openshift to perceptor format and send
	// to the perceptor
	imageInfo, err := mapper.NewPerceptorImageFromOSImage(image)
	if err != nil {
		return fmt.Errorf("error converting image to perceptor image: %v", err)
	}
	return communicator.SendPerceptorAddEvent(ic.imageURL, imageInfo)
}