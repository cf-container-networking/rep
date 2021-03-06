package rep

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"code.cloudfoundry.org/bbs/format"
	"code.cloudfoundry.org/bbs/models"
	"code.cloudfoundry.org/executor"
)

const (
	LifecycleTag  = "lifecycle"
	ResultFileTag = "result-file"
	DomainTag     = "domain"

	TaskLifecycle = "task"
	LRPLifecycle  = "lrp"

	ProcessGuidTag  = "process-guid"
	InstanceGuidTag = "instance-guid"
	ProcessIndexTag = "process-index"

	VolumeDriversTag = "volume-drivers"
	PlacementTagsTag = "placement-tags"
)

var (
	ErrContainerMissingTags = errors.New("container is missing tags")
	ErrInvalidProcessIndex  = errors.New("container does not have a valid process index")
)

func ActualLRPKeyFromTags(tags executor.Tags) (*models.ActualLRPKey, error) {
	if tags == nil {
		return &models.ActualLRPKey{}, ErrContainerMissingTags
	}

	processIndex, err := strconv.Atoi(tags[ProcessIndexTag])
	if err != nil {
		return &models.ActualLRPKey{}, ErrInvalidProcessIndex
	}

	actualLRPKey := models.NewActualLRPKey(
		tags[ProcessGuidTag],
		int32(processIndex),
		tags[DomainTag],
	)

	err = actualLRPKey.Validate()
	if err != nil {
		return &models.ActualLRPKey{}, err
	}

	return &actualLRPKey, nil
}

func ActualLRPInstanceKeyFromContainer(container executor.Container, cellID string) (*models.ActualLRPInstanceKey, error) {
	if container.Tags == nil {
		return &models.ActualLRPInstanceKey{}, ErrContainerMissingTags
	}

	actualLRPInstanceKey := models.NewActualLRPInstanceKey(
		container.Tags[InstanceGuidTag],
		cellID,
	)

	err := actualLRPInstanceKey.Validate()
	if err != nil {
		return &models.ActualLRPInstanceKey{}, err
	}

	return &actualLRPInstanceKey, nil
}

func ActualLRPNetInfoFromContainer(container executor.Container) (*models.ActualLRPNetInfo, error) {
	ports := []*models.PortMapping{}

	for _, port := range container.Ports {
		ports = append(ports, models.NewPortMappingWithTLSProxy(
			uint32(port.HostPort),
			uint32(port.ContainerPort),
			uint32(port.HostTLSProxyPort),
			uint32(port.ContainerTLSProxyPort),
		))
	}

	actualLRPNetInfo := models.NewActualLRPNetInfo(container.ExternalIP, container.InternalIP, ports...)

	err := actualLRPNetInfo.Validate()
	if err != nil {
		return nil, err
	}

	return &actualLRPNetInfo, nil
}

func LRPContainerGuid(processGuid, instanceGuid string) string {
	return instanceGuid
}

func NewRunRequestFromDesiredLRP(
	containerGuid string,
	desiredLRP *models.DesiredLRP,
	lrpKey *models.ActualLRPKey,
	lrpInstanceKey *models.ActualLRPInstanceKey,
) (executor.RunRequest, error) {
	desiredLRP = desiredLRP.VersionDownTo(format.V2)

	diskScope, err := diskScopeForRootFS(desiredLRP.RootFs)
	if err != nil {
		return executor.RunRequest{}, err
	}

	mounts, err := convertVolumeMounts(desiredLRP.VolumeMounts)
	if err != nil {
		return executor.RunRequest{}, err
	}

	runInfo := executor.RunInfo{
		CPUWeight: uint(desiredLRP.CpuWeight),
		DiskScope: diskScope,
		Ports:     ConvertPortMappings(desiredLRP.Ports),
		LogConfig: executor.LogConfig{
			Guid:       desiredLRP.LogGuid,
			Index:      int(lrpKey.Index),
			SourceName: desiredLRP.LogSource,
		},

		MetricsConfig: executor.MetricsConfig{
			Guid:  desiredLRP.MetricsGuid,
			Index: int(lrpKey.Index),
		},
		StartTimeoutMs:     uint(desiredLRP.StartTimeoutMs),
		Privileged:         desiredLRP.Privileged,
		CachedDependencies: ConvertCachedDependencies(desiredLRP.CachedDependencies),
		Setup:              desiredLRP.Setup,
		Action:             desiredLRP.Action,
		Monitor:            desiredLRP.Monitor,
		CheckDefinition:    desiredLRP.CheckDefinition,
		EgressRules:        desiredLRP.EgressRules,
		Env: append([]executor.EnvironmentVariable{
			{Name: "INSTANCE_GUID", Value: lrpInstanceKey.InstanceGuid},
			{Name: "INSTANCE_INDEX", Value: strconv.Itoa(int(lrpKey.Index))},
			{Name: "CF_INSTANCE_GUID", Value: lrpInstanceKey.InstanceGuid},
			{Name: "CF_INSTANCE_INDEX", Value: strconv.Itoa(int(lrpKey.Index))},
		}, executor.EnvironmentVariablesFromModel(desiredLRP.EnvironmentVariables)...),
		TrustedSystemCertificatesPath: desiredLRP.TrustedSystemCertificatesPath,
		VolumeMounts:                  mounts,
		Network:                       convertNetwork(desiredLRP.Network),
		CertificateProperties:         convertCertificateProperties(desiredLRP.CertificateProperties),
		ImageUsername:                 desiredLRP.ImageUsername,
		ImagePassword:                 desiredLRP.ImagePassword,
		EnableContainerProxy:          true,
	}

	// No need for the envoy proxy if there are no ports.  This flag controls the
	// step transformation (either prevent or include a run_step to run envoy) as
	// well as the proxy config handler to avoid generate the config
	// unecessarily.
	if len(runInfo.Ports) == 0 {
		runInfo.EnableContainerProxy = false
	}

	tags := executor.Tags{}
	return executor.NewRunRequest(containerGuid, &runInfo, tags), nil
}

func NewRunRequestFromTask(task *models.Task) (executor.RunRequest, error) {
	task = task.VersionDownTo(format.V2)

	diskScope, err := diskScopeForRootFS(task.RootFs)
	if err != nil {
		return executor.RunRequest{}, err
	}

	mounts, err := convertVolumeMounts(task.VolumeMounts)
	if err != nil {
		return executor.RunRequest{}, err
	}

	tags := executor.Tags{
		ResultFileTag: task.ResultFile,
	}
	runInfo := executor.RunInfo{
		DiskScope:  diskScope,
		CPUWeight:  uint(task.CpuWeight),
		Privileged: task.Privileged,
		LogConfig: executor.LogConfig{
			Guid:       task.LogGuid,
			SourceName: task.LogSource,
		},
		MetricsConfig: executor.MetricsConfig{
			Guid: task.MetricsGuid,
		},
		CachedDependencies:            ConvertCachedDependencies(task.CachedDependencies),
		Action:                        task.Action,
		Env:                           executor.EnvironmentVariablesFromModel(task.EnvironmentVariables),
		EgressRules:                   task.EgressRules,
		TrustedSystemCertificatesPath: task.TrustedSystemCertificatesPath,
		VolumeMounts:                  mounts,
		Network:                       convertNetwork(task.Network),
		CertificateProperties:         convertCertificateProperties(task.CertificateProperties),
		ImageUsername:                 task.ImageUsername,
		ImagePassword:                 task.ImagePassword,
		EnableContainerProxy:          false,
	}
	return executor.NewRunRequest(task.TaskGuid, &runInfo, tags), nil
}

func ConvertCachedDependencies(modelDeps []*models.CachedDependency) []executor.CachedDependency {
	execDeps := make([]executor.CachedDependency, len(modelDeps))
	for i := range modelDeps {
		execDeps[i] = ConvertCachedDependency(modelDeps[i])
	}
	return execDeps
}

func ConvertCachedDependency(modelDep *models.CachedDependency) executor.CachedDependency {
	return executor.CachedDependency{
		Name:              modelDep.Name,
		From:              modelDep.From,
		To:                modelDep.To,
		CacheKey:          modelDep.CacheKey,
		LogSource:         modelDep.LogSource,
		ChecksumValue:     modelDep.ChecksumValue,
		ChecksumAlgorithm: modelDep.ChecksumAlgorithm,
	}
}

func convertVolumeMounts(volumeMounts []*models.VolumeMount) ([]executor.VolumeMount, error) {
	execMnts := make([]executor.VolumeMount, len(volumeMounts))
	for i := range volumeMounts {
		var err error
		execMnts[i], err = convertVolumeMount(volumeMounts[i])
		if err != nil {
			return nil, err
		}
	}
	return execMnts, nil
}

func convertVolumeMount(volumeMnt *models.VolumeMount) (executor.VolumeMount, error) {
	var config map[string]interface{}

	if len(volumeMnt.Shared.MountConfig) > 0 {
		err := json.Unmarshal([]byte(volumeMnt.Shared.MountConfig), &config)
		if err != nil {
			return executor.VolumeMount{}, err
		}
	}

	var mode executor.BindMountMode
	switch volumeMnt.Mode {
	case "r":
		mode = executor.BindMountModeRO
	case "rw":
		mode = executor.BindMountModeRW
	default:
		return executor.VolumeMount{}, errors.New("unrecognized volume mount mode")
	}

	return executor.VolumeMount{
		Driver:        volumeMnt.Driver,
		VolumeId:      volumeMnt.Shared.VolumeId,
		ContainerPath: volumeMnt.ContainerDir,
		Mode:          mode,
		Config:        config,
	}, nil
}

func convertNetwork(network *models.Network) *executor.Network {
	if network == nil {
		return nil
	}

	return &executor.Network{
		Properties: network.Properties,
	}
}

func convertCertificateProperties(props *models.CertificateProperties) executor.CertificateProperties {
	if props == nil {
		return executor.CertificateProperties{}
	}

	return executor.CertificateProperties{
		OrganizationalUnit: props.OrganizationalUnit,
	}
}

func ConvertPortMappings(containerPorts []uint32) []executor.PortMapping {
	out := []executor.PortMapping{}
	for _, port := range containerPorts {
		out = append(out, executor.PortMapping{
			ContainerPort: uint16(port),
		})
	}

	return out
}

func IsPreloadedRootFS(rootFS string) (bool, error) {
	preloaded := false

	url, err := url.Parse(rootFS)
	if err != nil {
		return false, err
	}

	if url.Scheme == models.PreloadedRootFSScheme || url.Scheme == models.PreloadedOCIRootFSScheme {
		preloaded = true
	}

	return preloaded, nil
}

func diskScopeForRootFS(rootFS string) (executor.DiskLimitScope, error) {
	preloaded, err := IsPreloadedRootFS(rootFS)
	if err != nil {
		return executor.ExclusiveDiskLimit, err
	}

	if preloaded {
		return executor.ExclusiveDiskLimit, nil
	}
	return executor.TotalDiskLimit, nil
}
