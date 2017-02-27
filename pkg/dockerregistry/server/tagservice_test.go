package server

import (
	"fmt"
	"net/url"
	"reflect"
	"testing"

	"github.com/docker/distribution"
	"github.com/docker/distribution/context"
	"github.com/docker/distribution/digest"
	"github.com/docker/distribution/reference"

	"k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset/fake"

	"github.com/openshift/origin/pkg/client/testclient"
	registrytest "github.com/openshift/origin/pkg/dockerregistry/testutil"
	imagetest "github.com/openshift/origin/pkg/image/admission/testutil"
	imageapi "github.com/openshift/origin/pkg/image/api"
)

func createTestImageReactor(t *testing.T, client *testclient.Fake, serverURL *url.URL, namespace, repo string) *imageapi.Image {
	_, testManifest, _, err := registrytest.CreateRandomManifest(registrytest.ManifestSchema1, 3)
	if err != nil {
		t.Fatal(err)
	}

	_, testManifestSchema1, err := testManifest.Payload()
	if err != nil {
		t.Fatal(err)
	}

	testImage, err := registrytest.NewImageForManifest(
		fmt.Sprintf("%s/%s", namespace, repo),
		string(testManifestSchema1),
		false)
	if err != nil {
		t.Fatal(err)
	}
	testImage.DockerImageReference = fmt.Sprintf("%s/%s/%s@%s", serverURL.Host, namespace, repo, testImage.Name)

	client.AddReactor("get", "images", registrytest.GetFakeImageGetHandler(t, *testImage))

	return testImage
}

func createTestImageStreamReactor(t *testing.T, client *testclient.Fake, testImage *imageapi.Image, namespace, repo, tag string) *imageapi.ImageStream {
	testImageStream := registrytest.TestNewImageStreamObject(namespace, repo, tag, testImage.Name, testImage.DockerImageReference)
	if testImageStream.Annotations == nil {
		testImageStream.Annotations = make(map[string]string)
	}
	testImageStream.Annotations[imageapi.InsecureRepositoryAnnotation] = "true"

	client.AddReactor("get", "imagestreams", imagetest.GetFakeImageStreamGetHandler(t, *testImageStream))

	return testImageStream
}

func TestTagGet(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()
	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo, tag)

	testcases := []struct {
		title                 string
		tagName               string
		tagValue              distribution.Descriptor
		expectedError         bool
		expectedNotFoundError bool
		pullthrough           bool
		imageManaged          bool
	}{
		{
			title:        "get valid tag from managed image",
			tagName:      tag,
			tagValue:     distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			pullthrough:  true,
			imageManaged: true,
		},
		{
			title:        "get valid tag from managed image without pullthrough",
			tagName:      tag,
			tagValue:     distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			pullthrough:  false,
			imageManaged: true,
		},
		{
			title:                 "get valid tag from unmanaged image without pullthrough",
			tagName:               tag,
			pullthrough:           false,
			imageManaged:          false,
			expectedNotFoundError: true,
		},
		{
			title:                 "get missing tag",
			tagName:               tag + "-no-found",
			pullthrough:           true,
			imageManaged:          true,
			expectedError:         true,
			expectedNotFoundError: true,
		},
	}

	for _, tc := range testcases {
		if tc.imageManaged {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "true"
		} else {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "false"
		}

		localTagService := newTestTagService(nil)

		r := newTestRepositoryForPullthrough(t, ctx, nil, namespace, repo, client, tc.pullthrough)
		ts := &tagService{
			TagService: localTagService,
			repo:       r,
		}

		resultDesc, err := ts.Get(ctx, tc.tagName)

		switch err.(type) {
		case distribution.ErrTagUnknown:
			if !tc.expectedNotFoundError {
				t.Fatalf("[%s] unexpected error: %#+v", tc.title, err)
			}
		case nil:
			if tc.expectedError || tc.expectedNotFoundError {
				t.Fatalf("[%s] unexpected successful response", tc.title)
			}
		default:
			if tc.expectedError {
				break
			}
			t.Fatalf("[%s] unexpected error: %#+v", tc.title, err)
		}

		if resultDesc.Digest != tc.tagValue.Digest {
			t.Fatalf("[%s] unexpected result returned", tc.title)
		}
	}
}

func TestTagGetWithoutImageStream(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()

	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo+"-another", tag)

	testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "true"

	localTagService := newTestTagService(nil)

	named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
	if err != nil {
		t.Fatal(err)
	}

	r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, true)
	ts := &tagService{
		TagService: localTagService,
		repo:       r,
	}

	_, err = ts.Get(ctx, tag)
	if err == nil {
		t.Fatalf("error expected")
	}

	_, ok := err.(distribution.ErrRepositoryUnknown)
	if !ok {
		t.Fatalf("unexpected error: %#+v", err)
	}
}

func TestTagCreation(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()
	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo, tag)

	testcases := []struct {
		title         string
		tagName       string
		tagValue      distribution.Descriptor
		expectedError bool
		pullthrough   bool
		imageManaged  bool
	}{
		{
			title:        "create tag on managed image with pullthrough",
			tagName:      tag + "-new",
			tagValue:     distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			pullthrough:  true,
			imageManaged: true,
		},
		{
			title:         "create tag on unmanaged image without pullthrough",
			tagName:       tag + "-new",
			tagValue:      distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			expectedError: true,
		},
		{
			title:         "create tag on missing image",
			tagName:       tag + "-new",
			tagValue:      distribution.Descriptor{Digest: digest.Digest(etcdDigest)},
			expectedError: true,
		},
	}

	for _, tc := range testcases {
		if tc.imageManaged {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "true"
		} else {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "false"
		}

		localTagService := newTestTagService(nil)

		named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
		if err != nil {
			t.Fatal(err)
		}
		r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, tc.pullthrough)
		ts := &tagService{
			TagService: localTagService,
			repo:       r,
		}

		err = ts.Tag(ctx, tc.tagName, tc.tagValue)
		if tc.expectedError {
			if err == nil {
				t.Fatalf("[%s] error expected", tc.title)
			}
			continue
		}

		_, err = ts.Get(ctx, tc.tagName)
		if err == nil {
			t.Fatalf("error expected")
		}
	}
}

func TestTagCreationWithoutImageStream(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()

	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo+"-another", tag)

	localTagService := newTestTagService(nil)

	named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, true)
	ts := &tagService{
		TagService: localTagService,
		repo:       r,
	}

	err = ts.Tag(ctx, tag, distribution.Descriptor{Digest: digest.Digest(testImage.Name)})
	if err == nil {
		t.Fatalf("error expected")
	}

	_, ok := err.(distribution.ErrRepositoryUnknown)
	if !ok {
		t.Fatalf("unexpected error: %#+v", err)
	}
}

func TestTagDeletion(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()
	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo, tag)

	testcases := []struct {
		title                 string
		tagName               string
		tagValue              distribution.Descriptor
		expectedError         bool
		expectedNotFoundError bool
		pullthrough           bool
		imageManaged          bool
	}{
		{
			title:        "delete tag from managed image with pullthrough",
			tagName:      tag,
			pullthrough:  true,
			imageManaged: true,
		},
		{
			title:        "delete tag from managed image without pullthrough",
			tagName:      tag,
			imageManaged: true,
		},
		{
			title:       "delete tag from unmanaged image with pullthrough",
			tagName:     tag,
			pullthrough: true,
		},
		{
			title:                 "delete tag from unmanaged image without pullthrough",
			tagName:               tag,
			expectedNotFoundError: true,
		},
		{
			title:                 "delete wrong tag",
			tagName:               tag + "-not-found",
			expectedNotFoundError: true,
		},
	}

	for _, tc := range testcases {
		if tc.imageManaged {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "true"
		} else {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "false"
		}

		localTagService := newTestTagService(nil)

		named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
		if err != nil {
			t.Fatal(err)
		}

		r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, tc.pullthrough)
		ts := &tagService{
			TagService: localTagService,
			repo:       r,
		}

		err = ts.Untag(ctx, tc.tagName)

		switch err.(type) {
		case distribution.ErrTagUnknown:
			if !tc.expectedNotFoundError {
				t.Fatalf("[%s] unexpected error: %#+v", tc.title, err)
			}
		case nil:
			if tc.expectedError || tc.expectedNotFoundError {
				t.Fatalf("[%s] unexpected successful response", tc.title)
			}
		default:
			if tc.expectedError {
				break
			}
			t.Fatalf("[%s] unexpected error: %#+v", tc.title, err)
		}
	}
}

func TestTagDeletionWithoutImageStream(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()

	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo+"-another", tag)

	localTagService := newTestTagService(nil)

	named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, true)
	ts := &tagService{
		TagService: localTagService,
		repo:       r,
	}

	err = ts.Untag(ctx, tag)
	if err == nil {
		t.Fatalf("error expected")
	}

	_, ok := err.(distribution.ErrRepositoryUnknown)
	if !ok {
		t.Fatalf("unexpected error: %#+v", err)
	}
}

func TestTagGetAll(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()
	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo, tag)

	testcases := []struct {
		title         string
		expectResult  []string
		expectedError bool
		pullthrough   bool
		imageManaged  bool
	}{
		{
			title:        "get all tags with pullthrough",
			expectResult: []string{tag},
			pullthrough:  true,
			imageManaged: true,
		},
		{
			title:        "get all tags without pullthrough",
			expectResult: []string{tag},
			imageManaged: true,
		},
		{
			title:        "get all tags from unmanaged image without pullthrough",
			expectResult: []string{},
		},
	}

	for _, tc := range testcases {
		if tc.imageManaged {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "true"
		} else {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "false"
		}

		localTagService := newTestTagService(nil)

		named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
		if err != nil {
			t.Fatal(err)
		}
		r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, tc.pullthrough)
		ts := &tagService{
			TagService: localTagService,
			repo:       r,
		}

		result, err := ts.All(ctx)

		if err != nil && !tc.expectedError {
			t.Fatalf("[%s] unexpected error: %#+v", tc.title, err)
		}

		if !reflect.DeepEqual(result, tc.expectResult) {
			t.Fatalf("[%s] unexpected result: %#+v", tc.title, result)
		}
	}
}

func TestTagGetAllWithoutImageStream(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()

	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo+"-another", tag)

	localTagService := newTestTagService(nil)

	named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, true)
	ts := &tagService{
		TagService: localTagService,
		repo:       r,
	}

	_, err = ts.All(ctx)
	if err == nil {
		t.Fatalf("error expected")
	}

	_, ok := err.(distribution.ErrRepositoryUnknown)
	if !ok {
		t.Fatalf("unexpected error: %#+v", err)
	}
}

func TestTagLookup(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()
	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo, tag)

	testcases := []struct {
		title         string
		tagValue      distribution.Descriptor
		expectResult  []string
		expectedError bool
		pullthrough   bool
		imageManaged  bool
	}{
		{
			title:        "lookup tags with pullthrough",
			tagValue:     distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			expectResult: []string{tag},
			pullthrough:  true,
			imageManaged: true,
		},
		{
			title:        "lookup tags without pullthrough",
			tagValue:     distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			expectResult: []string{tag},
			imageManaged: true,
		},
		{
			title:        "lookup tags by missing digest",
			tagValue:     distribution.Descriptor{Digest: digest.Digest(etcdDigest)},
			expectResult: []string{},
			pullthrough:  true,
			imageManaged: true,
		},
		{
			title:        "lookup tags in unmanaged images without pullthrough",
			tagValue:     distribution.Descriptor{Digest: digest.Digest(testImage.Name)},
			expectResult: []string{},
		},
	}

	for _, tc := range testcases {
		if tc.imageManaged {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "true"
		} else {
			testImage.Annotations[imageapi.ManagedByOpenShiftAnnotation] = "false"
		}

		localTagService := newTestTagService(nil)

		named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
		if err != nil {
			t.Fatal(err)
		}
		r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, tc.pullthrough)
		ts := &tagService{
			TagService: localTagService,
			repo:       r,
		}

		result, err := ts.Lookup(ctx, tc.tagValue)

		if err != nil {
			if !tc.expectedError {
				t.Fatalf("[%s] unexpected error: %#+v", tc.title, err)
			}
			continue
		} else {
			if tc.expectedError {
				t.Fatalf("[%s] error expected", tc.title)
			}
		}

		if !reflect.DeepEqual(result, tc.expectResult) {
			t.Fatalf("[%s] unexpected result: %#+v", tc.title, result)
		}
	}
}

func TestTagLookupWithoutImageStream(t *testing.T) {
	namespace := "user"
	repo := "app"
	tag := "latest"
	client := &testclient.Fake{}

	// TODO: get rid of those nasty global vars
	backupRegistryClient := DefaultRegistryClient
	DefaultRegistryClient = makeFakeRegistryClient(client, fake.NewSimpleClientset())
	defer func() {
		// set it back once this test finishes to make other unit tests working again
		DefaultRegistryClient = backupRegistryClient
	}()

	ctx := context.Background()

	serverURL, _ := url.Parse("docker.io/centos")

	testImage := createTestImageReactor(t, client, serverURL, namespace, repo)
	createTestImageStreamReactor(t, client, testImage, namespace, repo+"-another", tag)

	localTagService := newTestTagService(nil)

	named, err := reference.ParseNamed(fmt.Sprintf("%s/%s", namespace, repo))
	if err != nil {
		t.Fatal(err)
	}

	r := newTestRepositoryForPullthrough(t, ctx, &testRepository{name: named}, namespace, repo, client, true)
	ts := &tagService{
		TagService: localTagService,
		repo:       r,
	}

	_, err = ts.Lookup(ctx, distribution.Descriptor{Digest: digest.Digest(testImage.Name)})
	if err == nil {
		t.Fatalf("error expected")
	}

	_, ok := err.(distribution.ErrRepositoryUnknown)
	if !ok {
		t.Fatalf("unexpected error: %#+v", err)
	}
}

type testRepository struct {
	distribution.Repository

	name reference.Named
}

func (r *testRepository) Named() reference.Named {
	return r.name
}

type testTagService struct {
	data  map[string]distribution.Descriptor
	calls map[string]int
}

func newTestTagService(data map[string]distribution.Descriptor) *testTagService {
	b := make(map[string]distribution.Descriptor)
	for d, content := range data {
		b[d] = content
	}
	return &testTagService{
		data:  b,
		calls: make(map[string]int),
	}
}

func (t *testTagService) Get(ctx context.Context, tag string) (distribution.Descriptor, error) {
	t.calls["Get"]++
	desc, exists := t.data[tag]
	if !exists {
		return distribution.Descriptor{}, distribution.ErrTagUnknown{Tag: tag}
	}
	return desc, nil
}

func (t *testTagService) Tag(ctx context.Context, tag string, desc distribution.Descriptor) error {
	t.calls["Tag"]++
	t.data[tag] = desc
	return nil
}

func (t *testTagService) Untag(ctx context.Context, tag string) error {
	t.calls["Untag"]++
	_, exists := t.data[tag]
	if !exists {
		return distribution.ErrTagUnknown{Tag: tag}
	}
	delete(t.data, tag)
	return nil
}

func (t *testTagService) All(ctx context.Context) (tags []string, err error) {
	t.calls["All"]++
	for tag := range t.data {
		tags = append(tags, tag)
	}
	return
}

func (t *testTagService) Lookup(ctx context.Context, desc distribution.Descriptor) (tags []string, err error) {
	t.calls["Lookup"]++
	for tag := range t.data {
		if t.data[tag].Digest == desc.Digest {
			tags = append(tags, tag)
		}
	}
	return
}
