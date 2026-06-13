// Port of tart's Credentials/DockerConfigCredentialsProvider.swift: reads
// ~/.docker/config.json and falls back to docker-credential-* helpers.
//go:build darwin

package credentials

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	"github.com/deploymenttheory/weave/internal/objcutil"
)

// DockerConfig ports the DockerConfig Codable struct.
type DockerConfig struct {
	Auths       map[string]DockerAuthConfig `json:"auths"`
	CredHelpers map[string]string           `json:"credHelpers"`
}

// FindCredHelper ports DockerConfig.findCredHelper(host:). Tart supports
// wildcards in credHelpers, similar to docker/cli#2928.
func (c *DockerConfig) FindCredHelper(host string) string {
	for hostPattern, helperProgram := range c.CredHelpers {
		if hostPattern == host {
			return helperProgram
		}
		compiledPattern, err := regexp.Compile(hostPattern)
		if err == nil && compiledPattern.FindString(host) == host {
			return helperProgram
		}
	}
	return ""
}

// DockerAuthConfig ports the DockerAuthConfig Codable struct.
type DockerAuthConfig struct {
	Auth string `json:"auth"`
}

// DecodeCredentials ports DockerAuthConfig.decodeCredentials(): auth is
// base64("username:password").
func (c DockerAuthConfig) DecodeCredentials() (string, string, bool) {
	if c.Auth == "" {
		return "", "", false
	}
	data, err := base64.StdEncoding.DecodeString(c.Auth)
	if err != nil {
		return "", "", false
	}
	components := strings.Split(string(data), ":")
	if len(components) != 2 {
		return "", "", false
	}
	return components[0], components[1], true
}

type dockerGetOutput struct {
	Username string `json:"Username"`
	Secret   string `json:"Secret"`
}

// DockerConfigCredentialsProvider ports the class of the same name.
type DockerConfigCredentialsProvider struct{}

var _ CredentialsProvider = (*DockerConfigCredentialsProvider)(nil)

func (p *DockerConfigCredentialsProvider) UserFriendlyName() string {
	return "Docker configuration credentials provider"
}

func (p *DockerConfigCredentialsProvider) Retrieve(host string) (string, string, bool, error) {
	dockerConfigURL := foundation.NSFileManagerDefaultManager().HomeDirectoryForCurrentUser().
		URLByAppendingPathComponent(objcutil.NSStr(".docker")).
		URLByAppendingPathComponent(objcutil.NSStr("config.json"))
	if !foundation.NSFileManagerDefaultManager().FileExistsAtPath(dockerConfigURL.Path()) {
		return "", "", false, nil
	}

	configData, err := os.ReadFile(objcutil.GoStr(dockerConfigURL.Path()))
	if err != nil {
		return "", "", false, err
	}
	var config DockerConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return "", "", false, err
	}

	if auth, ok := config.Auths[host]; ok {
		if user, password, ok := auth.DecodeCredentials(); ok {
			return user, password, true, nil
		}
	}
	if helperProgram := config.FindCredHelper(host); helperProgram != "" {
		return p.executeHelper("docker-credential-"+helperProgram, host)
	}

	return "", "", false, nil
}

func (p *DockerConfigCredentialsProvider) executeHelper(binaryName string, host string) (string, string, bool, error) {
	executableURL := objcutil.ResolveBinaryPath(binaryName)
	if executableURL == nil {
		return "", "", false, credentialsProviderFailed("%s not found in PATH", binaryName)
	}

	task := foundation.NSTaskFromID(purego.Send[purego.ID](purego.ID(purego.GetClass("NSTask")), purego.RegisterName("new")))
	task.SetExecutableURL(executableURL)
	task.SetArguments(objcutil.NSStringArray([]string{"get"}))

	outPipe := foundation.NSPipePipe()
	inPipe := foundation.NSPipePipe()
	task.SetStandardOutput(outPipe.Ptr())
	task.SetStandardError(outPipe.Ptr())
	task.SetStandardInput(inPipe.Ptr())

	if _, err := task.LaunchAndReturnError(); err != nil {
		return "", "", false, err
	}

	if _, err := inPipe.FileHandleForWriting().WriteDataError(objcutil.BytesToNSData([]byte(host + "\n"))); err != nil {
		return "", "", false, credentialsProviderFailed("Failed to write host to Docker helper!")
	}
	inPipe.FileHandleForWriting().CloseFile()

	outputNSData, err := outPipe.FileHandleForReading().ReadDataToEndOfFileAndReturnError()
	if err != nil {
		return "", "", false, err
	}
	outputData := objcutil.NSDataToBytes(outputNSData)

	task.WaitUntilExit()

	if !(task.TerminationReason() == foundation.NSTaskTerminationReasonExit && task.TerminationStatus() == 0) {
		if len(outputData) > 0 {
			fmt.Println(string(outputData))
		}
		return "", "", false, credentialsProviderFailed("Docker helper failed!")
	}
	if len(outputData) == 0 {
		return "", "", false, credentialsProviderFailed("Docker helper output is empty!")
	}

	var output dockerGetOutput
	if err := json.Unmarshal(outputData, &output); err != nil {
		return "", "", false, err
	}
	return output.Username, output.Secret, true, nil
}

func (p *DockerConfigCredentialsProvider) Store(host string, user string, password string) error {
	return credentialsProviderFailed("Docker helpers don't support storing!")
}
