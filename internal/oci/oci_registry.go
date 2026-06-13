// Port of tart's OCI/Registry.swift: the OCI Distribution registry client.
// URL/query manipulation uses net/url (Swift URLComponents is value-type
// string work); the HTTP transport itself stays on NSURLSession via Fetcher.
//go:build darwin

package oci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/deploymenttheory/weave/internal/ci"
	"github.com/deploymenttheory/weave/internal/credentials"
	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/fetcher"
	"github.com/deploymenttheory/weave/internal/objcutil"
	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/telemetry"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
)

// RegistryError ports tart's RegistryError enum.
type RegistryError struct {
	Message string
}

func (e *RegistryError) Error() string { return e.Message }

func registryErrorUnexpectedStatus(when string, code int, details string) *RegistryError {
	message := fmt.Sprintf("unexpected HTTP status code %d while %s", code, when)
	if details != "" {
		message += ": " + details
	}
	return &RegistryError{Message: message}
}

var errRegistryMissingLocationHeader = &RegistryError{Message: "missing Location header"}

func registryErrorAuthFailed(why string, details string) *RegistryError {
	message := "authentication failed: " + why
	if details != "" {
		message += ": " + details
	}
	return &RegistryError{Message: message}
}

func registryErrorMalformedHeader(why string) *RegistryError {
	return &RegistryError{Message: "malformed header: " + why}
}

// HTTP status codes used by the registry protocol (HTTPCode in Swift).
const (
	httpCodeOk             = 200
	httpCodeCreated        = 201
	httpCodeAccepted       = 202
	httpCodePartialContent = 206
	httpCodeUnauthorized   = 401
	httpCodeNotFound       = 404
)

// chunksAsData ports AsyncThrowingStream.asData(limitBytes:).
func chunksAsData(chunks <-chan fetcher.FetchChunk, limitBytes int64) ([]byte, error) {
	var result []byte
	for chunk := range chunks {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		result = append(result, chunk.Data...)
		if limitBytes > 0 && int64(len(result)) > limitBytes {
			return result, nil
		}
	}
	return result, nil
}

// TokenResponse ports tart's TokenResponse struct.
type TokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   *int   `json:"expires_in"`
	IssuedAtRaw string `json:"issued_at"`

	issuedAt time.Time
}

var _ Authentication = (*TokenResponse)(nil)

// ParseTokenResponse ports TokenResponse.parse(fromData:).
func ParseTokenResponse(data []byte) (*TokenResponse, error) {
	var response TokenResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}

	response.issuedAt = time.Now()
	if response.IssuedAtRaw != "" {
		if issuedAt, err := time.Parse(time.RFC3339, response.IssuedAtRaw); err == nil {
			response.issuedAt = issuedAt
		}
	}

	if response.Token == "" && response.AccessToken == "" {
		return nil, registryErrorAuthFailed("missing token or access_token. One must be present", "")
	}

	return &response, nil
}

// TokenExpiresAt ports TokenResponse.tokenExpiresAt. Tokens default to 60
// seconds of validity when expires_in is omitted, per the Docker token
// authentication specification.
func (t *TokenResponse) TokenExpiresAt() time.Time {
	expiresIn := 60
	if t.ExpiresIn != nil {
		expiresIn = *t.ExpiresIn
	}
	return t.issuedAt.Add(time.Duration(expiresIn) * time.Second)
}

func (t *TokenResponse) Header() (string, string) {
	token := t.Token
	if token == "" {
		token = t.AccessToken
	}
	return "Authorization", "Bearer " + token
}

func (t *TokenResponse) IsValid() bool {
	return time.Now().Before(t.TokenExpiresAt())
}

// Registry ports tart's Registry class.
type Registry struct {
	baseURL              *url.URL
	Namespace            string
	CredentialsProviders []credentials.CredentialsProvider
	authenticationKeeper AuthenticationKeeper
}

func defaultCredentialsProviders() []credentials.CredentialsProvider {
	return []credentials.CredentialsProvider{
		&credentials.EnvironmentCredentialsProvider{},
		&credentials.DockerConfigCredentialsProvider{},
		&credentials.KeychainCredentialsProvider{},
	}
}

// NewRegistryWithBaseURL ports Registry.init(baseURL:namespace:
// credentialsProviders:). Pass nil for the default provider chain.
func NewRegistryWithBaseURL(baseURL *url.URL, namespace string, credentialsProviders []credentials.CredentialsProvider) *Registry {
	if credentialsProviders == nil {
		credentialsProviders = defaultCredentialsProviders()
	}
	return &Registry{
		baseURL:              baseURL,
		Namespace:            namespace,
		CredentialsProviders: credentialsProviders,
	}
}

// NewRegistry ports the convenience Registry.init(host:namespace:insecure:
// credentialsProviders:).
func NewRegistry(host string, namespace string, insecure bool, credentialsProviders []credentials.CredentialsProvider) (*Registry, error) {
	proto := "https"
	if insecure {
		proto = "http"
	}

	baseURL, err := url.Parse(proto + "://" + host + "/v2/")
	if err != nil || baseURL.Host == "" {
		hint := ""
		if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
			hint = ", make sure that it doesn't start with http:// or https://"
		}
		return nil, weaveerrors.ErrImproperlyFormattedHost(host, hint)
	}

	return NewRegistryWithBaseURL(baseURL, namespace, credentialsProviders), nil
}

// Host ports Registry.host.
func (r *Registry) Host() string {
	return r.baseURL.Host
}

// Ping ports Registry.ping().
func (r *Registry) Ping(ctx context.Context) error {
	_, response, err := r.dataRequest(ctx, "GET", r.endpointURL("/v2/"), nil, nil, nil, true)
	if err != nil {
		return err
	}
	if code := int(response.StatusCode()); code != httpCodeOk {
		return registryErrorUnexpectedStatus("doing ping", code, "")
	}
	return nil
}

// PushManifest ports Registry.pushManifest(reference:manifest:).
func (r *Registry) PushManifest(ctx context.Context, reference string, manifest OCIManifest) (string, error) {
	manifestJSON, err := manifest.ToJSON()
	if err != nil {
		return "", err
	}

	data, response, err := r.dataRequest(ctx, "PUT", r.endpointURL(r.Namespace+"/manifests/"+reference),
		map[string]string{"Content-Type": manifest.MediaType}, nil, manifestJSON, true)
	if err != nil {
		return "", err
	}
	if code := int(response.StatusCode()); code != httpCodeCreated {
		return "", registryErrorUnexpectedStatus("pushing manifest", code, objcutil.TextPreview(data))
	}

	return DigestHash(manifestJSON), nil
}

// PullManifest ports Registry.pullManifest(reference:).
func (r *Registry) PullManifest(ctx context.Context, reference string) (OCIManifest, []byte, error) {
	data, response, err := r.dataRequest(ctx, "GET", r.endpointURL(r.Namespace+"/manifests/"+reference),
		map[string]string{"Accept": ociManifestMediaType}, nil, nil, true)
	if err != nil {
		return OCIManifest{}, nil, err
	}
	if code := int(response.StatusCode()); code != httpCodeOk {
		return OCIManifest{}, nil, registryErrorUnexpectedStatus("pulling manifest", code, objcutil.TextPreview(data))
	}

	manifest, err := NewOCIManifestFromJSON(data)
	if err != nil {
		return OCIManifest{}, nil, err
	}
	return manifest, data, nil
}

func (r *Registry) uploadLocationFromResponse(response *foundation.NSHTTPURLResponse) (*url.URL, error) {
	locationRaw := response.ValueForHTTPHeaderField(objcutil.NSStr("Location"))
	if locationRaw == nil {
		return nil, errRegistryMissingLocationHeader
	}

	location, err := url.Parse(objcutil.GoStr(locationRaw))
	if err != nil {
		return nil, registryErrorMalformedHeader(fmt.Sprintf("Location header contains invalid URL: %q", objcutil.GoStr(locationRaw)))
	}

	// URL.absolutize(_ baseURL:) equivalent.
	return r.baseURL.ResolveReference(location), nil
}

// PushBlob ports Registry.pushBlob(fromData:chunkSizeMb:digest:); an empty
// digest computes it from the data, like the Swift default argument.
func (r *Registry) PushBlob(ctx context.Context, fromData []byte, chunkSizeMb int, digest string) (string, error) {
	ctx, span := otel.Tracer("weave").Start(ctx, "oci.push_blob",
		trace.WithAttributes(
			attribute.String("oci.registry", r.Host()),
			attribute.String("oci.namespace", r.Namespace),
			attribute.Int64("oci.bytes", int64(len(fromData))),
		))
	defer span.End()
	telemetry.OTelShared().Instruments.OCIBytesTransferred.Record(ctx, int64(len(fromData)),
		metric.WithAttributes(attribute.String("direction", "push"), attribute.String("oci.registry", r.Host())))

	// Initiate a blob upload.
	data, postResponse, err := r.dataRequest(ctx, "POST", r.endpointURL(r.Namespace+"/blobs/uploads/"),
		map[string]string{"Content-Length": "0"}, nil, nil, true)
	if err != nil {
		return "", err
	}
	if code := int(postResponse.StatusCode()); code != httpCodeAccepted {
		return "", registryErrorUnexpectedStatus("pushing blob (POST)", code, objcutil.TextPreview(data))
	}

	// Figure out where to upload the blob.
	uploadLocation, err := r.uploadLocationFromResponse(postResponse)
	if err != nil {
		return "", err
	}

	if digest == "" {
		digest = DigestHash(fromData)
	}

	if chunkSizeMb == 0 {
		// Monolithic upload.
		data, response, err := r.dataRequest(ctx, "PUT", uploadLocation,
			map[string]string{"Content-Type": "application/octet-stream"},
			map[string]string{"digest": digest}, fromData, true)
		if err != nil {
			return "", err
		}
		if code := int(response.StatusCode()); code != httpCodeCreated {
			return "", registryErrorUnexpectedStatus(fmt.Sprintf("pushing blob (PUT) to %s", uploadLocation), code, objcutil.TextPreview(data))
		}
		return digest, nil
	}

	// Chunked upload.
	uploadedBytes := 0
	chunkSize := chunkSizeMb * 1_000_000
	for uploadedBytes < len(fromData) || uploadedBytes == 0 {
		end := min(uploadedBytes+chunkSize, len(fromData))
		chunk := fromData[uploadedBytes:end]
		lastChunk := end == len(fromData)

		method := "PATCH"
		var parameters map[string]string
		if lastChunk {
			method = "PUT"
			parameters = map[string]string{"digest": digest}
		}

		data, response, err := r.dataRequest(ctx, method, uploadLocation,
			map[string]string{
				"Content-Type":  "application/octet-stream",
				"Content-Range": fmt.Sprintf("%d-%d", uploadedBytes, uploadedBytes+len(chunk)-1),
			}, parameters, chunk, true)
		if err != nil {
			return "", err
		}
		// Always accept both statuses since AWS ECR is not following the
		// specification.
		if code := int(response.StatusCode()); code != httpCodeCreated && code != httpCodeAccepted {
			return "", registryErrorUnexpectedStatus(fmt.Sprintf("streaming blob to %s", uploadLocation), code, objcutil.TextPreview(data))
		}

		uploadedBytes += len(chunk)
		if lastChunk {
			break
		}

		// Update location for the next chunk.
		uploadLocation, err = r.uploadLocationFromResponse(response)
		if err != nil {
			return "", err
		}
	}

	return digest, nil
}

// BlobExists ports Registry.blobExists(_:).
func (r *Registry) BlobExists(ctx context.Context, digest string) (bool, error) {
	data, response, err := r.dataRequest(ctx, "HEAD", r.endpointURL(r.Namespace+"/blobs/"+digest), nil, nil, nil, true)
	if err != nil {
		return false, err
	}

	switch code := int(response.StatusCode()); code {
	case httpCodeOk:
		return true, nil
	case httpCodeNotFound:
		return false, nil
	default:
		return false, registryErrorUnexpectedStatus("checking blob", code, objcutil.TextPreview(data))
	}
}

// PullBlob ports Registry.pullBlob(_:rangeStart:handler:).
func (r *Registry) PullBlob(ctx context.Context, digest string, rangeStart int64, handler func([]byte) error) error {
	ctx, span := otel.Tracer("weave").Start(ctx, "oci.pull_blob",
		trace.WithAttributes(
			attribute.String("oci.registry", r.Host()),
			attribute.String("oci.namespace", r.Namespace),
			attribute.String("oci.digest", digest),
		))
	defer span.End()

	var pulledBytes int64
	wrappedHandler := func(data []byte) error {
		pulledBytes += int64(len(data))
		return handler(data)
	}

	expectedStatusCode := httpCodeOk
	headers := map[string]string{}

	// Send a Range header and expect HTTP 206 in return. Do not send it at
	// all when rangeStart is 0: it makes no sense and we might get HTTP 200.
	if rangeStart != 0 {
		expectedStatusCode = httpCodePartialContent
		headers["Range"] = fmt.Sprintf("bytes=%d-", rangeStart)
	}

	chunks, response, err := r.channelRequest(ctx, "GET", r.endpointURL(r.Namespace+"/blobs/"+digest),
		headers, nil, nil, true, true)
	if err != nil {
		return err
	}
	if code := int(response.StatusCode()); code != expectedStatusCode {
		body, _ := chunksAsData(chunks, 4096)
		return registryErrorUnexpectedStatus("pulling blob", code, objcutil.TextPreview(body))
	}

	for chunk := range chunks {
		if chunk.Err != nil {
			return chunk.Err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := wrappedHandler(chunk.Data); err != nil {
			return err
		}
	}

	telemetry.OTelShared().Instruments.OCIBytesTransferred.Record(ctx, pulledBytes,
		metric.WithAttributes(attribute.String("direction", "pull"), attribute.String("oci.registry", r.Host())))
	span.SetAttributes(attribute.Int64("oci.bytes", pulledBytes))
	return nil
}

func (r *Registry) endpointURL(endpoint string) *url.URL {
	relative, err := url.Parse(endpoint)
	if err != nil {
		return r.baseURL
	}
	return r.baseURL.ResolveReference(relative)
}

func (r *Registry) dataRequest(ctx context.Context, method string, endpoint *url.URL,
	headers map[string]string, parameters map[string]string, body []byte, doAuth bool) ([]byte, *foundation.NSHTTPURLResponse, error) {
	chunks, response, err := r.channelRequest(ctx, method, endpoint, headers, parameters, body, doAuth, false)
	if err != nil {
		return nil, nil, err
	}

	data, err := chunksAsData(chunks, 0)
	if err != nil {
		return nil, nil, err
	}
	return data, response, nil
}

func (r *Registry) channelRequest(ctx context.Context, method string, endpoint *url.URL,
	headers map[string]string, parameters map[string]string, body []byte, doAuth bool, viaFile bool) (<-chan fetcher.FetchChunk, *foundation.NSHTTPURLResponse, error) {
	requestURL := *endpoint
	if len(parameters) > 0 {
		query := requestURL.Query()
		for key, value := range parameters {
			query.Add(key, value)
		}
		requestURL.RawQuery = query.Encode()
	}

	buildRequest := func() *foundation.NSMutableURLRequest {
		request := foundation.NSMutableURLRequestFromID(purego.Send[purego.ID](
			objcutil.AllocClass("NSMutableURLRequest"), purego.RegisterName("initWithURL:"),
			foundation.NSURLURLWithString(objcutil.NSStr(requestURL.String())).Ptr()))
		request.SetHTTPMethod(objcutil.NSStr(method))
		for key, value := range headers {
			request.AddValueForHTTPHeaderField(objcutil.NSStr(value), objcutil.NSStr(key))
		}
		if body != nil {
			request.AddValueForHTTPHeaderField(objcutil.NSStr(strconv.Itoa(len(body))), objcutil.NSStr("Content-Length"))
			request.SetHTTPBody(objcutil.BytesToNSData(body))
		}
		return request
	}

	chunks, response, err := r.authAwareRequest(ctx, buildRequest(), viaFile, doAuth)
	if err != nil {
		return nil, nil, err
	}

	if doAuth && int(response.StatusCode()) == httpCodeUnauthorized {
		if err := r.auth(ctx, response); err != nil {
			return nil, nil, err
		}
		chunks, response, err = r.authAwareRequest(ctx, buildRequest(), viaFile, doAuth)
		if err != nil {
			return nil, nil, err
		}
	}

	return chunks, response, nil
}

func (r *Registry) auth(ctx context.Context, response *foundation.NSHTTPURLResponse) error {
	// Process the WWW-Authenticate header.
	wwwAuthenticateRaw := response.ValueForHTTPHeaderField(objcutil.NSStr("WWW-Authenticate"))
	if wwwAuthenticateRaw == nil {
		return registryErrorAuthFailed("got HTTP 401, but WWW-Authenticate header is missing", "")
	}

	wwwAuthenticate, err := NewWWWAuthenticate(objcutil.GoStr(wwwAuthenticateRaw))
	if err != nil {
		return err
	}

	if strings.ToLower(wwwAuthenticate.Scheme) == "basic" {
		if user, password, ok := r.lookupCredentials(); ok {
			r.authenticationKeeper.Set(BasicAuthentication{User: user, Password: password})
		}
		return nil
	}

	if strings.ToLower(wwwAuthenticate.Scheme) != "bearer" {
		return registryErrorAuthFailed(fmt.Sprintf(
			"WWW-Authenticate header's authentication scheme %q is unsupported, expected \"Bearer\" scheme",
			wwwAuthenticate.Scheme), "")
	}
	realm, ok := wwwAuthenticate.KVs["realm"]
	if !ok {
		return registryErrorAuthFailed("WWW-Authenticate header is missing a \"realm\" directive", "")
	}

	// Request a token per the Docker Token Authentication Specification:
	// the client makes a GET request using the service and scope values
	// from the WWW-Authenticate header.
	authenticateURL, err := url.Parse(realm)
	if err != nil {
		return registryErrorAuthFailed(fmt.Sprintf(
			"WWW-Authenticate header's realm directive %q doesn't look like URL", realm), "")
	}

	query := url.Values{}
	for _, key := range []string{"scope", "service"} {
		if value, ok := wwwAuthenticate.KVs[key]; ok {
			query.Add(key, value)
		}
	}
	authenticateURL.RawQuery = query.Encode()

	headers := map[string]string{}
	if user, password, ok := r.lookupCredentials(); ok {
		encodedCredentials := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		headers["Authorization"] = "Basic " + encodedCredentials
	}

	data, tokenResponse, err := r.dataRequest(ctx, "GET", authenticateURL, headers, nil, nil, false)
	if err != nil {
		return err
	}
	if code := int(tokenResponse.StatusCode()); code != httpCodeOk {
		return registryErrorAuthFailed(fmt.Sprintf(
			"received unexpected HTTP status code %d while retrieving an authentication token", code), objcutil.TextPreview(data))
	}

	token, err := ParseTokenResponse(data)
	if err != nil {
		return err
	}
	r.authenticationKeeper.Set(token)

	return nil
}

func (r *Registry) lookupCredentials() (string, string, bool) {
	host := r.baseURL.Host

	for _, provider := range r.CredentialsProviders {
		user, password, ok, err := provider.Retrieve(host)
		if err != nil {
			fmt.Printf("Failed to retrieve credentials using %s, authentication may fail: %v\n",
				provider.UserFriendlyName(), err)
			continue
		}
		if ok {
			return user, password, true
		}
	}
	return "", "", false
}

func (r *Registry) authAwareRequest(ctx context.Context, request *foundation.NSMutableURLRequest,
	viaFile bool, doAuth bool) (<-chan fetcher.FetchChunk, *foundation.NSHTTPURLResponse, error) {
	if doAuth {
		if name, value, ok := r.authenticationKeeper.Header(); ok {
			request.AddValueForHTTPHeaderField(objcutil.NSStr(value), objcutil.NSStr(name))
		}
	}

	request.SetValueForHTTPHeaderField(
		objcutil.NSStr(fmt.Sprintf("Weave/%s (%s; %s)", ci.CIVersion(), weaveplatform.DeviceInfoOS(), weaveplatform.DeviceInfoModel())),
		objcutil.NSStr("User-Agent"))

	return fetcher.FetcherFetch(ctx, &request.NSURLRequest, viaFile)
}

// TagsList implements the OCI distribution tags-list endpoint on the
// existing Registry client (auth and TLS handling reused).
func (r *Registry) TagsList(ctx context.Context) ([]string, error) {
	var tags []string
	endpoint := r.endpointURL(r.Namespace + "/tags/list")
	parameters := map[string]string(nil)

	// The endpoint is paginated via the Link header; a simple n+last cursor
	// is universally supported.
	for {
		data, response, err := r.dataRequest(ctx, "GET", endpoint, nil, parameters, nil, true)
		if err != nil {
			return nil, err
		}
		if code := int(response.StatusCode()); code != httpCodeOk {
			return nil, registryErrorUnexpectedStatus("listing tags", code, objcutil.TextPreview(data))
		}

		var parsed struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, err
		}
		tags = append(tags, parsed.Tags...)

		next := objcutil.GoStr(response.ValueForHTTPHeaderField(objcutil.NSStr("Link")))
		if next == "" || len(parsed.Tags) == 0 {
			return tags, nil
		}
		parameters = map[string]string{
			"n":    "1000",
			"last": parsed.Tags[len(parsed.Tags)-1],
		}
	}
}
