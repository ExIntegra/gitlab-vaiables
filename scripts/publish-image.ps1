[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$')]
    [string]$Version,

    [ValidatePattern('^[a-z0-9]+(?:[._-][a-z0-9]+)*$')]
    [string]$DockerHubUsername = $env:DOCKERHUB_USERNAME,

    [ValidatePattern('^[a-z0-9]+(?:[._-][a-z0-9]+)*$')]
    [string]$ImageName = 'ci-variables',

    [ValidatePattern('^[a-z0-9][a-z0-9_.-]{0,63}$')]
    [string]$Builder = 'gitlabvars-builder',

    [ValidateSet('linux/amd64,linux/arm64', 'linux/amd64', 'linux/arm64')]
    [string]$Platforms = 'linux/amd64,linux/arm64',

    [switch]$Push
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($DockerHubUsername)) {
    throw 'Set DOCKERHUB_USERNAME or pass -DockerHubUsername with your Docker Hub account or organization.'
}

$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$repository = "docker.io/$DockerHubUsername/$ImageName"
$stableVersion = -not $Version.Contains('-')
$versionTag = "${repository}:$Version"
$archiveDirectory = Join-Path $projectRoot 'artifacts'
$archivePath = Join-Path $archiveDirectory "$ImageName-$Version.oci.tar"
$destination = if ($Push) { $repository } else { $archivePath }
$operation = if ($Push) { "Build and publish $versionTag ($Platforms)" } else { "Build OCI archive $versionTag ($Platforms); do not publish" }

# -WhatIf performs no Docker call, file write, login, build or publication.
if (-not $PSCmdlet.ShouldProcess($destination, $operation)) { return }

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Docker CLI is not installed or is not in PATH.' }
& docker version --format '{{.Server.Version}}'
if ($LASTEXITCODE -ne 0) { throw 'Docker is not running or is not accessible.' }
& docker buildx version
if ($LASTEXITCODE -ne 0) { throw 'Docker Buildx is not available.' }

$builders = & docker buildx ls --format '{{json .}}'
if ($LASTEXITCODE -ne 0) { throw 'Cannot list Docker builders.' }
$selectedBuilder = $builders | ForEach-Object { $_ | ConvertFrom-Json } | Where-Object { $_.Name -eq $Builder } | Select-Object -First 1
if (-not $selectedBuilder) {
    & docker buildx create --name $Builder --driver docker-container
    if ($LASTEXITCODE -ne 0) { throw 'Cannot create the image builder.' }
} elseif ($selectedBuilder.Driver -ne 'docker-container') {
    throw "Builder $Builder does not use docker-container. Choose another name with -Builder."
}

$revision = 'unknown'
if ((Test-Path -LiteralPath (Join-Path $projectRoot '.git')) -and (Get-Command git -ErrorAction SilentlyContinue)) {
    $gitRevision = & git -C $projectRoot rev-parse HEAD
    if ($LASTEXITCODE -eq 0 -and $gitRevision -match '^[0-9a-f]{40,64}$') { $revision = $gitRevision.Trim() }
}
$created = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
$buildArguments = @(
    'buildx', 'build',
    '--builder', $Builder,
    '--platform', $Platforms,
    '--file', (Join-Path $projectRoot 'Dockerfile'),
    '--tag', $versionTag,
    '--build-arg', "VERSION=$Version",
    '--build-arg', "REVISION=$revision",
    '--build-arg', "CREATED=$created",
    '--provenance=mode=min',
    '--sbom=true'
)
if ($stableVersion) { $buildArguments += @('--tag', "${repository}:latest") }
if ($Push) {
    # Authenticate separately with `docker login`; credentials are not script arguments.
    $buildArguments += '--push'
} else {
    if (Test-Path -LiteralPath $archivePath) { throw "Archive already exists: $archivePath. Choose another version or move the existing archive." }
    New-Item -ItemType Directory -Path $archiveDirectory -Force | Out-Null
    $buildArguments += @('--output', "type=oci,dest=$archivePath")
}
$buildArguments += $projectRoot
& docker @buildArguments
if ($LASTEXITCODE -ne 0) { throw 'Image build failed. Nothing is retried automatically.' }

if ($Push) {
    Write-Output "Published: $versionTag"
    Write-Output "Verify platforms: docker buildx imagetools inspect $versionTag"
} else {
    Write-Output "OCI archive created: $archivePath"
    Write-Output 'No image was published to Docker Hub.'
}
