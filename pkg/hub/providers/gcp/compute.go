// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
)

const (
	defaultComputeBaseURL = "https://compute.googleapis.com/compute/v1"
	cloudPlatformScope    = "https://www.googleapis.com/auth/cloud-platform"
	defaultHubID          = "grain-registry"
	InstanceHTTPPort      = 8080
)

// ErrOutOfScope is returned when a compute, IAM, SQL, storage, or secret operation
// targets a resource outside the registry's configured project, zone, prefix, or label scope.
var ErrOutOfScope = errors.New("gcp provider: resource is outside configured hub scope")

// ComputeOption configures a GCECompute provider.
type ComputeOption func(*GCECompute)

// WithComputeHTTPClient overrides the HTTP client used for GCE REST API calls.
func WithComputeHTTPClient(hc *http.Client) ComputeOption {
	return func(c *GCECompute) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithComputeBaseURL overrides the GCE v1 REST API base URL (for tests).
func WithComputeBaseURL(baseURL string) ComputeOption {
	return func(c *GCECompute) {
		if v := strings.TrimSpace(baseURL); v != "" {
			c.baseURL = strings.TrimRight(v, "/")
		}
	}
}

// WithComputeHubID overrides the `grain-registry` label value enforced on VMs and disks.
func WithComputeHubID(id string) ComputeOption {
	return func(c *GCECompute) {
		if v := strings.TrimSpace(id); v != "" {
			c.hubID = v
		}
	}
}

// WithOperationPollInterval overrides the polling interval when waiting on GCE zone operations.
func WithOperationPollInterval(d time.Duration) ComputeOption {
	return func(c *GCECompute) {
		if d > 0 {
			c.pollInterval = d
		}
	}
}

// GCECompute implements `registry.Compute` against the Google Compute Engine v1 REST API,
// enforcing project, zone, prefix (`vm_prefix`), and label (`grain-registry`, `grain-entry`,
// `grain-generation`) scope checks on every operation.
type GCECompute struct {
	project       string
	region        string
	zones         []string
	zoneSet       map[string]bool
	network       string
	subnetwork    string
	bootImage     string
	startupScript string
	vmPrefix      string
	hubID         string
	labels        map[string]string
	baseURL       string
	httpClient    *http.Client
	pollInterval  time.Duration
}

var gceMachineTypeRe = regexp.MustCompile(`^[a-z][a-z0-9]*-(standard|highmem|highcpu|megamem|ultramem|custom)-[0-9]+(-[a-z0-9]+)*$`)

// ErrNoNestedVirtualization is wrapped by ValidateMachineType when a machine
// type cannot host nested virtualization. Hosted VMs are always created with
// advancedMachineFeatures.enableNestedVirtualization so grain can run Granule
// microVMs and gVisor KVM sandboxes on /dev/kvm, and GCE rejects that (or the
// VM's sandboxes fail at runtime) on E2 machine types.
var ErrNoNestedVirtualization = errors.New("machine type does not support nested virtualization")

// BaseMachineType extracts the bare GCE machine type name (e.g. "n2-standard-4" or "c4-standard-8")
// from either a bare name or a zonal/project resource path.
func BaseMachineType(machineType string) string {
	mt := strings.TrimSpace(machineType)
	if idx := strings.LastIndex(mt, "/"); idx >= 0 {
		mt = mt[idx+1:]
	}
	return mt
}

// ValidateMachineType checks that `machineType` is a valid GCE machine type for a
// hosted VM (for example `n2-standard-*` or `c4-standard-*`). E2 machine types are
// rejected because they do not support nested virtualization.
func ValidateMachineType(machineType string) error {
	base := BaseMachineType(machineType)
	if base == "" {
		return errors.New("gcp compute: machine_type is required")
	}
	if strings.HasPrefix(base, "e2-") {
		return fmt.Errorf("gcp compute: machine_type %q: %w; use an N2 or C4 machine type (for example n2-standard-4 or c4-standard-4)", machineType, ErrNoNestedVirtualization)
	}
	if !gceMachineTypeRe.MatchString(base) {
		return fmt.Errorf("gcp compute: invalid machine_type %q", machineType)
	}
	return nil
}

// NewGCECompute constructs a GCECompute provider from `cfg`.
func NewGCECompute(ctx context.Context, cfg hub.ComputeConfig, opts ...ComputeOption) (*GCECompute, error) {
	project := strings.TrimSpace(cfg.Project)
	if project == "" {
		return nil, errors.New("gcp compute: compute.project is required")
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		return nil, errors.New("gcp compute: compute.region is required")
	}
	if len(cfg.Zones) == 0 {
		return nil, errors.New("gcp compute: compute.zones must be non-empty")
	}
	vmPrefix := strings.TrimSpace(cfg.VMPrefix)
	if vmPrefix == "" {
		vmPrefix = "grain-vm-"
	}
	zoneSet := make(map[string]bool, len(cfg.Zones))
	zones := make([]string, 0, len(cfg.Zones))
	for _, z := range cfg.Zones {
		z = strings.TrimSpace(z)
		if z == "" {
			continue
		}
		if !strings.HasPrefix(z, region+"-") {
			return nil, fmt.Errorf("gcp compute: zone %q does not belong to region %q", z, region)
		}
		if !zoneSet[z] {
			zoneSet[z] = true
			zones = append(zones, z)
		}
	}
	if len(zones) == 0 {
		return nil, errors.New("gcp compute: compute.zones must contain at least one valid zone")
	}
	for _, sh := range cfg.Shapes {
		if strings.TrimSpace(sh.MachineType) != "" {
			if err := ValidateMachineType(sh.MachineType); err != nil {
				return nil, fmt.Errorf("gcp compute: shape %q: %w", sh.Name, err)
			}
		}
	}

	startupScript := cfg.StartupScript
	if scriptFile := strings.TrimSpace(cfg.StartupScriptFile); scriptFile != "" {
		b, err := os.ReadFile(scriptFile)
		if err != nil {
			return nil, fmt.Errorf("gcp compute: reading startup_script_file %s: %w", scriptFile, err)
		}
		startupScript = string(b)
	}

	regID := strings.TrimSpace(cfg.RegistryID)
	if regID == "" && cfg.Labels != nil {
		regID = strings.TrimSpace(cfg.Labels["grain-registry"])
	}
	if regID == "" {
		regID = defaultHubID
	}

	labelsCopy := make(map[string]string, len(cfg.Labels))
	for k, v := range cfg.Labels {
		labelsCopy[k] = v
	}

	c := &GCECompute{
		project:       project,
		region:        region,
		zones:         zones,
		zoneSet:       zoneSet,
		network:       strings.TrimSpace(cfg.Network),
		subnetwork:    strings.TrimSpace(cfg.Subnetwork),
		bootImage:     strings.TrimSpace(cfg.BootImage),
		startupScript: startupScript,
		vmPrefix:      vmPrefix,
		hubID:         regID,
		labels:        labelsCopy,
		baseURL:       defaultComputeBaseURL,
		pollInterval:  50 * time.Millisecond,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.httpClient == nil {
		ts, err := google.DefaultTokenSource(ctx, cloudPlatformScope)
		if err != nil {
			return nil, fmt.Errorf("gcp compute: resolving Application Default Credentials: %w", err)
		}
		c.httpClient = oauth2.NewClient(ctx, ts)
		c.httpClient.Timeout = 30 * time.Second
	}
	return c, nil
}

// VMNameForEntry returns the canonical GCE instance name `<vm_prefix><entryName>`.
func (c *GCECompute) VMNameForEntry(entryName string) (string, error) {
	entryName = strings.ToLower(strings.TrimSpace(entryName))
	if err := api.ValidateEntryName(entryName); err != nil {
		return "", fmt.Errorf("%w: invalid entry name %q: %v", ErrOutOfScope, entryName, err)
	}
	return c.vmPrefix + entryName, nil
}

func (c *GCECompute) checkZone(zone string) error {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return fmt.Errorf("%w: empty zone", ErrOutOfScope)
	}
	if !strings.HasPrefix(zone, c.region+"-") || !c.zoneSet[zone] {
		return fmt.Errorf("%w: zone %q is not in configured zones %v", ErrOutOfScope, zone, c.zones)
	}
	return nil
}

func (c *GCECompute) checkInstanceName(instanceID string) (string, error) {
	instanceID = strings.TrimSpace(instanceID)
	if !strings.HasPrefix(instanceID, c.vmPrefix) {
		return "", fmt.Errorf("%w: instance %q does not have required prefix %q", ErrOutOfScope, instanceID, c.vmPrefix)
	}
	entryName := strings.TrimPrefix(instanceID, c.vmPrefix)
	if err := api.ValidateEntryName(entryName); err != nil {
		return "", fmt.Errorf("%w: instance %q suffix %q is not a valid entry name: %v", ErrOutOfScope, instanceID, entryName, err)
	}
	return entryName, nil
}

func (c *GCECompute) verifyInstanceLabels(inst *gceInstance, expectedEntry string) error {
	if inst == nil {
		return fmt.Errorf("%w: nil instance", ErrOutOfScope)
	}
	if !strings.HasPrefix(inst.Name, c.vmPrefix) {
		return fmt.Errorf("%w: instance %q does not match vm_prefix %q", ErrOutOfScope, inst.Name, c.vmPrefix)
	}
	gotReg := ""
	if inst.Labels != nil {
		gotReg = inst.Labels["grain-registry"]
	}
	if gotReg != c.hubID {
		return fmt.Errorf("%w: instance %q label grain-registry=%q does not match %q", ErrOutOfScope, inst.Name, gotReg, c.hubID)
	}
	if inst.Labels != nil {
		if gotEntry := inst.Labels["grain-entry"]; gotEntry != "" {
			if expectedEntry != "" && gotEntry != expectedEntry {
				return fmt.Errorf("%w: instance %q label grain-entry=%q does not match %q", ErrOutOfScope, inst.Name, gotEntry, expectedEntry)
			}
			if inst.Name != c.vmPrefix+gotEntry {
				return fmt.Errorf("%w: instance %q does not match grain-entry label %q", ErrOutOfScope, inst.Name, gotEntry)
			}
		}
	}
	return nil
}

type gceInstance struct {
	ID                      string                      `json:"id,omitempty"`
	Name                    string                      `json:"name"`
	Zone                    string                      `json:"zone,omitempty"`
	MachineType             string                      `json:"machineType,omitempty"`
	Status                  string                      `json:"status,omitempty"`
	Labels                  map[string]string           `json:"labels,omitempty"`
	Tags                    *gceTags                    `json:"tags,omitempty"`
	Disks                   []gceAttachedDisk           `json:"disks,omitempty"`
	NetworkInterfaces       []gceNetworkInterface       `json:"networkInterfaces,omitempty"`
	ServiceAccounts         []gceServiceAccount         `json:"serviceAccounts,omitempty"`
	Metadata                *gceMetadata                `json:"metadata,omitempty"`
	ShieldedInstanceConfig  *gceShieldedConfig          `json:"shieldedInstanceConfig,omitempty"`
	AdvancedMachineFeatures *gceAdvancedMachineFeatures `json:"advancedMachineFeatures,omitempty"`
}

type gceAdvancedMachineFeatures struct {
	EnableNestedVirtualization bool `json:"enableNestedVirtualization,omitempty"`
}

type gceTags struct {
	Items []string `json:"items,omitempty"`
}

type gceAttachedDisk struct {
	Boot             bool               `json:"boot"`
	AutoDelete       bool               `json:"autoDelete"`
	Type             string             `json:"type,omitempty"`
	Mode             string             `json:"mode,omitempty"`
	InitializeParams *gceDiskInitParams `json:"initializeParams,omitempty"`
}

type gceDiskInitParams struct {
	DiskName        string            `json:"diskName,omitempty"`
	DiskSizeGb      int64             `json:"diskSizeGb,omitempty"`
	DiskType        string            `json:"diskType,omitempty"`
	ProvisionedIops int64             `json:"provisionedIops,omitempty"`
	SourceImage     string            `json:"sourceImage,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type gceNetworkInterface struct {
	Network       string            `json:"network,omitempty"`
	Subnetwork    string            `json:"subnetwork,omitempty"`
	NetworkIP     string            `json:"networkIP,omitempty"`
	NicType       string            `json:"nicType,omitempty"`
	AccessConfigs []gceAccessConfig `json:"accessConfigs,omitempty"`
}

type gceAccessConfig struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

type gceServiceAccount struct {
	Email  string   `json:"email"`
	Scopes []string `json:"scopes"`
}

type gceMetadata struct {
	Items []gceMetadataItem `json:"items,omitempty"`
}

type gceMetadataItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type gceShieldedConfig struct {
	EnableSecureBoot          bool `json:"enableSecureBoot"`
	EnableVtpm                bool `json:"enableVtpm"`
	EnableIntegrityMonitoring bool `json:"enableIntegrityMonitoring"`
}

type gceOperation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  *struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error,omitempty"`
}

type gceInstanceList struct {
	Items []gceInstance `json:"items"`
}

type gceDisk struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

type gceDiskList struct {
	Items []gceDisk `json:"items"`
}

// CreateInstance provisions a hosted GCE VM instance for `spec`, or adopts an existing VM
// if one with matching `grain-registry`, `grain-entry`, and `grain-generation` labels already exists.
func (c *GCECompute) CreateInstance(ctx context.Context, spec hub.InstanceSpec) (*hub.InstanceHandle, error) {
	vmName, err := c.VMNameForEntry(spec.Name)
	if err != nil {
		return nil, err
	}
	zone := strings.TrimSpace(spec.Zone)
	if zone == "" && len(c.zones) > 0 {
		zone = c.zones[0]
	}
	if err := c.checkZone(zone); err != nil {
		return nil, err
	}

	gen := ""
	if spec.Labels != nil {
		gen = strings.TrimSpace(spec.Labels["grain-generation"])
	}
	if gen == "" && spec.Generation > 0 {
		gen = strconv.FormatInt(spec.Generation, 10)
	}
	if gen == "" {
		gen = "1"
	}

	existing, found, err := c.getRawInstance(ctx, zone, vmName)
	if err != nil {
		return nil, err
	}
	if found {
		if err := c.verifyInstanceLabels(existing, spec.Name); err != nil {
			return nil, err
		}
		gotGen := ""
		if existing.Labels != nil {
			gotGen = existing.Labels["grain-generation"]
		}
		if gotGen == gen && existing.Status != "TERMINATED" && existing.Status != "STOPPING" {
			ip := extractInternalIP(existing)
			backendURL := ""
			if ip != "" {
				backendURL = fmt.Sprintf("http://%s:%d", ip, InstanceHTTPPort)
			}
			return &hub.InstanceHandle{
				InstanceID: vmName,
				Zone:       zone,
				InternalIP: ip,
				BackendURL: backendURL,
			}, nil
		}
		// Existing VM is from an older generation or terminated; delete it before inserting the new generation.
		if err := c.DeleteInstance(ctx, zone, vmName); err != nil {
			return nil, fmt.Errorf("gcp compute: deleting stale generation VM %s: %w", vmName, err)
		}
	}

	labels := make(map[string]string, len(c.labels)+len(spec.Labels)+4)
	for k, v := range c.labels {
		labels[k] = v
	}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["grain-registry"] = c.hubID
	labels["grain-entry"] = spec.Name
	labels["grain-generation"] = gen
	labels["grain-managed"] = "true"

	machineType := strings.TrimSpace(spec.Shape.MachineType)
	if machineType == "" {
		machineType = "n2-standard-4"
	}
	if err := ValidateMachineType(machineType); err != nil {
		return nil, err
	}
	baseMT := BaseMachineType(machineType)
	if !strings.Contains(machineType, "/") {
		machineType = fmt.Sprintf("zones/%s/machineTypes/%s", zone, machineType)
	}
	diskGB := int64(spec.BootDiskGB)
	if diskGB <= 0 {
		diskGB = int64(spec.Shape.BootDiskGB)
	}
	if diskGB <= 0 {
		diskGB = 50
	}
	diskTypeName := strings.TrimSpace(spec.Shape.BootDiskType)
	if diskTypeName == "" {
		if strings.HasPrefix(baseMT, "c4-") {
			diskTypeName = "hyperdisk-balanced"
		} else {
			diskTypeName = "pd-balanced"
		}
	}
	diskType := diskTypeName
	if !strings.Contains(diskType, "/") {
		diskType = fmt.Sprintf("zones/%s/diskTypes/%s", zone, diskType)
	}
	bootImage := strings.TrimSpace(spec.BootImage)
	if bootImage == "" {
		bootImage = c.bootImage
	}
	if bootImage == "" {
		bootImage = "projects/debian-cloud/global/images/family/debian-12"
	}

	envJSON, err := marshalDeterministicEnv(spec.StartupEnv)
	if err != nil {
		return nil, fmt.Errorf("gcp compute: marshaling startup env: %w", err)
	}

	metaItems := []gceMetadataItem{
		{Key: "enable-oslogin", Value: "TRUE"},
		{Key: "block-project-ssh-keys", Value: "TRUE"},
		{Key: "grain-registry", Value: c.hubID},
		{Key: "grain-entry", Value: spec.Name},
		{Key: "grain-generation", Value: gen},
		{Key: "grain-config", Value: envJSON},
	}
	if c.startupScript != "" {
		metaItems = append(metaItems, gceMetadataItem{
			Key:   "startup-script",
			Value: c.startupScript,
		})
	}

	netName := c.network
	if netName != "" && !strings.Contains(netName, "/") {
		netName = fmt.Sprintf("projects/%s/global/networks/%s", c.project, netName)
	}
	subnetName := c.subnetwork
	if subnetName != "" && !strings.Contains(subnetName, "/") {
		subnetName = fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", c.project, c.region, subnetName)
	}
	netIface := gceNetworkInterface{
		Network:       netName,
		Subnetwork:    subnetName,
		AccessConfigs: nil, // Hosted VMs never have external IPs.
	}
	if strings.HasPrefix(baseMT, "c4-") || strings.HasPrefix(baseMT, "c3-") {
		netIface.NicType = "GVNIC"
	}

	var saList []gceServiceAccount
	if saEmail := strings.TrimSpace(spec.ServiceAccount); saEmail != "" {
		saList = []gceServiceAccount{
			{
				Email:  saEmail,
				Scopes: []string{cloudPlatformScope},
			},
		}
	}

	insertReq := gceInstance{
		Name:        vmName,
		MachineType: machineType,
		Labels:      labels,
		Tags: &gceTags{
			Items: []string{"grain-instance"},
		},
		Disks: []gceAttachedDisk{
			{
				Boot:       true,
				AutoDelete: true,
				Type:       "PERSISTENT",
				Mode:       "READ_WRITE",
				InitializeParams: &gceDiskInitParams{
					DiskName:        vmName,
					DiskSizeGb:      diskGB,
					DiskType:        diskType,
					ProvisionedIops: int64(spec.Shape.BootDiskIOPS),
					SourceImage:     bootImage,
					Labels:          labels,
				},
			},
		},
		NetworkInterfaces: []gceNetworkInterface{netIface},
		ServiceAccounts:   saList,
		Metadata:          &gceMetadata{Items: metaItems},
		ShieldedInstanceConfig: &gceShieldedConfig{
			EnableSecureBoot:          true,
			EnableVtpm:                true,
			EnableIntegrityMonitoring: true,
		},
		AdvancedMachineFeatures: &gceAdvancedMachineFeatures{
			EnableNestedVirtualization: true,
		},
	}

	insertURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances",
		c.baseURL, url.PathEscape(c.project), url.PathEscape(zone))
	if err := c.doOperation(ctx, http.MethodPost, insertURL, insertReq); err != nil {
		if isServiceAccountNotFound(err, spec.ServiceAccount) {
			// The account was most likely created moments ago and has not propagated to
			// Compute yet. Mark the error so the reconciler retries with backoff rather than
			// failing the entry; the insert operation failed, so no VM was left behind.
			return nil, &serviceAccountNotReadyError{err: fmt.Errorf("gcp compute: inserting instance %s in %s: %w", vmName, zone, err)}
		}
		if isServiceAccountAccessDenied(err) {
			// The registry's actAs grant on the account (compute.grant_self_act_as) was
			// most likely made moments ago and IAM has not applied it yet; it takes up to
			// a minute or so. Retry like an unpropagated account, but say what is missing
			// in case it never arrives.
			return nil, &serviceAccountNotReadyError{err: fmt.Errorf(
				"gcp compute: inserting instance %s in %s: %w (the hub needs iam.serviceAccounts.actAs on %s: with compute.grant_self_act_as on it grants itself %s there, and a new binding can take a minute to apply)",
				vmName, zone, err, spec.ServiceAccount, "roles/iam.serviceAccountUser")}
		}
		return nil, fmt.Errorf("gcp compute: inserting instance %s in %s: %w", vmName, zone, err)
	}

	created, found, err := c.getRawInstance(ctx, zone, vmName)
	if err != nil {
		return nil, fmt.Errorf("gcp compute: reading created instance %s: %w", vmName, err)
	}
	if !found {
		return nil, fmt.Errorf("gcp compute: instance %s not found after creation", vmName)
	}
	ip := extractInternalIP(created)
	backendURL := ""
	if ip != "" {
		backendURL = fmt.Sprintf("http://%s:%d", ip, InstanceHTTPPort)
	}
	return &hub.InstanceHandle{
		InstanceID: vmName,
		Zone:       zone,
		InternalIP: ip,
		BackendURL: backendURL,
	}, nil
}

// StopInstance stops a running GCE VM instance after verifying zone, prefix, and `grain-registry` label scope.
func (c *GCECompute) StopInstance(ctx context.Context, zone, instanceID string) error {
	if err := c.checkZone(zone); err != nil {
		return err
	}
	entryName, err := c.checkInstanceName(instanceID)
	if err != nil {
		return err
	}
	existing, found, err := c.getRawInstance(ctx, zone, instanceID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if err := c.verifyInstanceLabels(existing, entryName); err != nil {
		return err
	}
	if existing.Status == "TERMINATED" || existing.Status == "STOPPED" {
		return nil
	}
	stopURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s/stop",
		c.baseURL, url.PathEscape(c.project), url.PathEscape(zone), url.PathEscape(instanceID))
	if err := c.doOperation(ctx, http.MethodPost, stopURL, nil); err != nil {
		return fmt.Errorf("gcp compute: stopping instance %s in %s: %w", instanceID, zone, err)
	}
	return nil
}

// DeleteInstance deletes a GCE VM instance (if present) after verifying zone, prefix, and
// `grain-registry` label scope, and sweeps any straggler disks labelled for the entry.
func (c *GCECompute) DeleteInstance(ctx context.Context, zone, instanceID string) error {
	if err := c.checkZone(zone); err != nil {
		return err
	}
	entryName, err := c.checkInstanceName(instanceID)
	if err != nil {
		return err
	}
	existing, found, err := c.getRawInstance(ctx, zone, instanceID)
	if err != nil {
		return err
	}
	if found {
		if err := c.verifyInstanceLabels(existing, entryName); err != nil {
			return err
		}
		delURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s",
			c.baseURL, url.PathEscape(c.project), url.PathEscape(zone), url.PathEscape(instanceID))
		if err := c.doOperation(ctx, http.MethodDelete, delURL, nil); err != nil {
			return fmt.Errorf("gcp compute: deleting instance %s in %s: %w", instanceID, zone, err)
		}
	}

	if err := c.sweepStragglerDisks(ctx, zone, entryName); err != nil {
		return err
	}
	return nil
}

func (c *GCECompute) sweepStragglerDisks(ctx context.Context, zone, entryName string) error {
	disksURL := fmt.Sprintf("%s/projects/%s/zones/%s/disks",
		c.baseURL, url.PathEscape(c.project), url.PathEscape(zone))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, disksURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gcp compute: listing disks in %s: %w", zone, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("gcp compute: listing disks HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var list gceDiskList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return fmt.Errorf("gcp compute: decoding disk list: %w", err)
	}
	for _, d := range list.Items {
		if !strings.HasPrefix(d.Name, c.vmPrefix) || d.Labels == nil {
			continue
		}
		if d.Labels["grain-registry"] != c.hubID || d.Labels["grain-entry"] != entryName {
			continue
		}
		delDiskURL := fmt.Sprintf("%s/projects/%s/zones/%s/disks/%s",
			c.baseURL, url.PathEscape(c.project), url.PathEscape(zone), url.PathEscape(d.Name))
		if err := c.doOperation(ctx, http.MethodDelete, delDiskURL, nil); err != nil {
			return fmt.Errorf("gcp compute: deleting straggler disk %s: %w", d.Name, err)
		}
	}
	return nil
}

// GetInstanceStatus inspects the live status of a GCE VM instance in `zone`.
// If the VM does not exist (`404 Not Found`), `State` is returned as `"NOT_FOUND"`.
func (c *GCECompute) GetInstanceStatus(ctx context.Context, zone, instanceID string) (*hub.InstanceStatus, error) {
	if err := c.checkZone(zone); err != nil {
		return nil, err
	}
	entryName, err := c.checkInstanceName(instanceID)
	if err != nil {
		return nil, err
	}
	inst, found, err := c.getRawInstance(ctx, zone, instanceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return &hub.InstanceStatus{
			InstanceID: instanceID,
			Name:       instanceID,
			Zone:       zone,
			State:      "NOT_FOUND",
		}, nil
	}
	if err := c.verifyInstanceLabels(inst, entryName); err != nil {
		return nil, err
	}
	ip := extractInternalIP(inst)
	backendURL := ""
	if ip != "" {
		backendURL = fmt.Sprintf("http://%s:%d", ip, InstanceHTTPPort)
	}
	return &hub.InstanceStatus{
		InstanceID: inst.Name,
		Name:       inst.Name,
		Zone:       zone,
		State:      inst.Status,
		InternalIP: ip,
		BackendURL: backendURL,
		Labels:     inst.Labels,
	}, nil
}

// ListInstances lists all GCE VMs across the configured zones whose name starts with
// `vm_prefix` and whose `grain-registry` label matches this registry's ID.
func (c *GCECompute) ListInstances(ctx context.Context) ([]hub.InstanceStatus, error) {
	var out []hub.InstanceStatus
	for _, zone := range c.zones {
		listURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances",
			c.baseURL, url.PathEscape(c.project), url.PathEscape(zone))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("gcp compute: listing instances in %s: %w", zone, err)
		}
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("gcp compute: listing instances in %s HTTP %d: %s", zone, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
		}
		var list gceInstanceList
		if err := json.Unmarshal(bodyBytes, &list); err != nil {
			return nil, fmt.Errorf("gcp compute: decoding instance list in %s: %w", zone, err)
		}
		for i := range list.Items {
			inst := &list.Items[i]
			if !strings.HasPrefix(inst.Name, c.vmPrefix) || inst.Labels == nil {
				continue
			}
			if inst.Labels["grain-registry"] != c.hubID {
				continue
			}
			ip := extractInternalIP(inst)
			backendURL := ""
			if ip != "" {
				backendURL = fmt.Sprintf("http://%s:%d", ip, InstanceHTTPPort)
			}
			out = append(out, hub.InstanceStatus{
				InstanceID: inst.Name,
				Name:       inst.Name,
				Zone:       zone,
				State:      inst.Status,
				InternalIP: ip,
				BackendURL: backendURL,
				Labels:     inst.Labels,
			})
		}
	}
	return out, nil
}

func (c *GCECompute) getRawInstance(ctx context.Context, zone, instanceID string) (*gceInstance, bool, error) {
	getURL := fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s",
		c.baseURL, url.PathEscape(c.project), url.PathEscape(zone), url.PathEscape(instanceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("gcp compute: GET %s: %w", instanceID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("gcp compute: GET %s HTTP %d: %s", instanceID, resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	var inst gceInstance
	if err := json.Unmarshal(bodyBytes, &inst); err != nil {
		return nil, false, fmt.Errorf("gcp compute: decoding instance %s: %w", instanceID, err)
	}
	return &inst, true, nil
}

func (c *GCECompute) doOperation(ctx context.Context, method, reqURL string, payload any) error {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if method == http.MethodDelete && resp.StatusCode == http.StatusNotFound {
		return nil
	}
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	if len(bytes.TrimSpace(respBytes)) == 0 {
		return nil
	}
	var op gceOperation
	if err := json.Unmarshal(respBytes, &op); err != nil {
		return nil
	}
	if err := checkOpError(&op); err != nil {
		return err
	}
	if op.Status == "" || op.Status == "DONE" || op.Name == "" {
		return nil
	}
	// Extract zone from reqURL if present to poll zone operation.
	zone := extractZoneFromURL(reqURL)
	if zone == "" {
		return nil
	}
	return c.waitZoneOperation(ctx, zone, op.Name)
}

func (c *GCECompute) waitZoneOperation(ctx context.Context, zone, opName string) error {
	opURL := fmt.Sprintf("%s/projects/%s/zones/%s/operations/%s",
		c.baseURL, url.PathEscape(c.project), url.PathEscape(zone), url.PathEscape(opName))
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, opURL, nil)
			if err != nil {
				return err
			}
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("polling operation %s HTTP %d: %s", opName, resp.StatusCode, strings.TrimSpace(string(b)))
			}
			var op gceOperation
			if err := json.Unmarshal(b, &op); err != nil {
				return err
			}
			if err := checkOpError(&op); err != nil {
				return err
			}
			if op.Status == "DONE" {
				return nil
			}
		}
	}
}

func checkOpError(op *gceOperation) error {
	if op == nil || op.Error == nil || len(op.Error.Errors) == 0 {
		return nil
	}
	e := op.Error.Errors[0]
	return fmt.Errorf("GCE operation %s failed (%s): %s", op.Name, e.Code, e.Message)
}

// serviceAccountNotReadyError keeps GCE's own message while also matching
// registry.ErrServiceAccountNotReady under errors.Is.
type serviceAccountNotReadyError struct{ err error }

func (e *serviceAccountNotReadyError) Error() string { return e.err.Error() }

func (e *serviceAccountNotReadyError) Unwrap() []error {
	return []error{e.err, hub.ErrServiceAccountNotReady}
}

// isServiceAccountNotFound reports whether an instances.insert failure says the instance's
// service account does not exist (yet). GCE reports this either as a failed operation with
// code EXTERNAL_RESOURCE_NOT_FOUND ("The resource '<sa>' of type 'serviceAccount' was not
// found.") or as a synchronous 4xx naming the account; both are what an eventually
// consistent, just-created account looks like from Compute's side.
func isServiceAccountNotFound(err error, saEmail string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	sa := strings.ToLower(strings.TrimSpace(saEmail))
	mentionsSA := strings.Contains(msg, "serviceaccount") ||
		strings.Contains(msg, "service account") ||
		(sa != "" && strings.Contains(msg, sa))
	if !mentionsSA {
		return false
	}
	return strings.Contains(msg, "external_resource_not_found") ||
		strings.Contains(msg, "was not found") ||
		strings.Contains(msg, "notfound") ||
		strings.Contains(msg, "does not exist")
}

// isServiceAccountAccessDenied reports whether an instances.insert failure is GCE refusing
// to attach the instance's service account because the caller lacks
// iam.serviceAccounts.actAs on it: a failed operation with code
// SERVICE_ACCOUNT_ACCESS_DENIED ("The user does not have access to service account
// '<sa>'..."), or the same message on a synchronous 4xx.
func isServiceAccountAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "service_account_access_denied") ||
		strings.Contains(msg, "does not have access to service account")
}

func extractZoneFromURL(rawURL string) string {
	idx := strings.Index(rawURL, "/zones/")
	if idx < 0 {
		return ""
	}
	rest := rawURL[idx+len("/zones/"):]
	if slash := strings.IndexByte(rest, '/'); slash > 0 {
		return rest[:slash]
	}
	return rest
}

func extractInternalIP(inst *gceInstance) string {
	if inst == nil || len(inst.NetworkInterfaces) == 0 {
		return ""
	}
	return strings.TrimSpace(inst.NetworkInterfaces[0].NetworkIP)
}

func marshalDeterministicEnv(env map[string]string) (string, error) {
	if len(env) == 0 {
		return "{}", nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(env))
	for _, k := range keys {
		ordered[k] = env[k]
	}
	b, err := json.Marshal(ordered)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func init() {
	hub.RegisterComputeProvider("gce", func(ctx context.Context, cfg hub.ComputeConfig) (hub.Compute, error) {
		return NewGCECompute(ctx, cfg)
	})
}
