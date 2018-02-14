package dumper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/blackducksoftware/perceivers/image/pkg/mapper"

	perceptorapi "github.com/blackducksoftware/perceptor/pkg/api"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	imageclient "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"

	log "github.com/sirupsen/logrus"
)

// ImageDumper handles sending all images to the perceptor periodically
type ImageDumper struct {
	client       *imageclient.ImageV1Client
	allImagesURL string
}

// NewImageDumper creates a new ImageDumper object
func NewImageDumper(ic *imageclient.ImageV1Client, perceptorURL string) *ImageDumper {
	return &ImageDumper{
		client:       ic,
		allImagesURL: fmt.Sprintf("%s/%s", perceptorURL, perceptorapi.AllImagesPath),
	}
}

// Run starts a controller that will send all images to the perceptor periodically
func (id *ImageDumper) Run(interval time.Duration, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		time.Sleep(interval)

		// Get all the images in the format pereptor uses
		images, err := id.getAllImagesAsPerceptorImages()
		if err != nil {
			log.Errorf("unable to get all images: %v", err)
			continue
		}
		log.Infof("about to PUT all images -- found %d images", len(images))

		jsonBytes, err := json.Marshal(perceptorapi.NewAllImages(images))
		if err != nil {
			log.Errorf("unable to serialize all images: %v", err)
			continue
		}

		// Send all the image information to the perceptor
		req, err := http.NewRequest("PUT", id.allImagesURL, bytes.NewBuffer(jsonBytes))
		if err != nil {
			log.Errorf("unable to create PUT request for %s: %v", id.allImagesURL, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Errorf("unable to PUT to %s: %v", id.allImagesURL, err)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			log.Infof("http POST request to %s succeeded", id.allImagesURL)
		} else {
			log.Errorf("http POST request to %s failed with status code %d", id.allImagesURL, resp.StatusCode)
		}
	}
}

func (id *ImageDumper) getAllImagesAsPerceptorImages() ([]perceptorapi.Image, error) {
	perceptorImages := []perceptorapi.Image{}

	// Get all images from openshift
	images, err := id.client.Images().List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Translate the images from openshift to perceptor format
	for _, image := range images.Items {
		perceptorImage, err := mapper.NewPerceptorImageFromOSImage(&image)
		if err != nil {
			return nil, err
		}
		perceptorImages = append(perceptorImages, *perceptorImage)
	}
	return perceptorImages, nil
}