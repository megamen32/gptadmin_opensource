#Requires -Version 5.1
param([switch]$LiveMetadata, [string]$PublishedMatrix, [string]$PublishedArchive)
$ErrorActionPreference = 'Stop'
$installer = (Resolve-Path (Join-Path $PSScriptRoot '../../../deploy/install_win.ps1')).Path
$tokens = $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw $errors[0] }
$functions = @('Get-WindowsPackageArch', 'Get-WindowsReleaseText', 'Get-WindowsReleaseCandidates', 'Resolve-WindowsReleasePackage', 'Assert-WindowsPackage')
foreach ($name in $functions) {
    $fn = $ast.Find({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq $name }, $true)
    if (-not $fn) { throw "Missing installer function $name." }
    . ([scriptblock]::Create($fn.Extent.Text))
}
$arch = Get-WindowsPackageArch
if ($arch -notin @('x64','arm64')) { throw "Unexpected native architecture $arch." }
$fixtureReleaseOrigin = 'https://github.com/megamen32/gptadmin_opensource/releases'
$fixture = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N') + '.zip')
try {
    [IO.File]::WriteAllBytes($fixture, [byte[]](11,23,35))
    $sha = (Get-FileHash -Algorithm SHA256 -LiteralPath $fixture).Hash.ToLowerInvariant()
    $matrix = @{schema='gptadmin.release-matrix/v2'; build_version=102; artifacts=@()}
    foreach ($platform in @('x64','arm64')) {
        foreach ($edition in @('client','full')) {
            $matrix.artifacts += @{platform='windows'; arch=$platform; edition=$edition; file="gptadmin-windows-$platform-$edition.zip"; size=3; sha256=$sha}
        }
    }
    $json = $matrix | ConvertTo-Json -Depth 5
    $hasMatrix = @(@{name='gptadmin-release-matrix.json'})
    & {
        function Get-WindowsReleaseText([string]$Uri) {
            if ($Uri -match 'api.github.com.+page=1$') {
                $first = @(1..99 | ForEach-Object { @{tag_name="node-$_"; draft=$false; prerelease=$false; assets=@()} })
                $first += @{tag_name='v9'; draft=$false; prerelease=$false; assets=$hasMatrix}
                return (ConvertTo-Json -InputObject $first -Depth 5)
            }
            if ($Uri -match 'api.github.com.+page=2$') {
                $second = @(
                    @{tag_name='v109'; draft=$true; prerelease=$false; assets=$hasMatrix}
                    @{tag_name='v108'; draft=$false; prerelease=$true; assets=$hasMatrix}
                    @{tag_name='v107'; draft=$false; prerelease=$false; assets=@()}
                    @{tag_name='v102'; draft=$false; prerelease=$false; assets=$hasMatrix}
                )
                return (ConvertTo-Json -InputObject $second -Depth 5)
            }
            if ($Uri -eq "$fixtureReleaseOrigin/download/v102/gptadmin-release-matrix.json") { return $json }
            throw "Unexpected fixture URL $Uri."
        }
        foreach ($platform in @('x64','arm64')) {
            foreach ($edition in @('client','full')) {
                $got = Resolve-WindowsReleasePackage $edition $platform
                if ($got.Url -cne "$fixtureReleaseOrigin/download/v102/gptadmin-windows-$platform-$edition.zip") { throw "Incorrect role or architecture: $($got.Url)" }
                Assert-WindowsPackage $fixture $got
            }
        }
        $matrix.artifacts = @($matrix.artifacts | Where-Object { $_.edition -ne 'client' })
        $json = $matrix | ConvertTo-Json -Depth 5
        try { $null = Resolve-WindowsReleasePackage 'client' 'x64'; throw 'Missing client was accepted.' }
        catch { if ($_.Exception.Message -notlike '*must contain one verified*') { throw } }
    }
    & {
        function Get-WindowsReleaseText([string]$Uri) {
            if ($Uri -like 'https://api.github.com/*') { throw 'Anonymous GitHub API rate limited.' }
            if ($Uri -eq 'https://github.com/megamen32/gptadmin_opensource/releases.atom') {
                return "<feed xmlns='http://www.w3.org/2005/Atom'><entry><link rel='alternate' href='$fixtureReleaseOrigin/tag/v102'/></entry><entry><link rel='alternate' href='$fixtureReleaseOrigin/tag/v104'/></entry><entry><link rel='alternate' href='https://example.invalid/tag/v1000'/></entry></feed>"
            }
            if ($Uri -eq "$fixtureReleaseOrigin/tag/v104") { return '<a>Pre-release</a>' }
            if ($Uri -eq "$fixtureReleaseOrigin/tag/v102") { return '<h1>v102</h1>' }
            if ($Uri -eq "$fixtureReleaseOrigin/download/v102/gptadmin-release-matrix.json") { return $json }
            throw "Unexpected fallback URL $Uri."
        }
        $result = Resolve-WindowsReleasePackage 'full' 'x64'
        if ($result.Tag -cne 'v102') { throw 'Anonymous Atom fallback selected the wrong release.' }
        [IO.File]::WriteAllBytes($fixture, [byte[]](11,23,36))
        try { Assert-WindowsPackage $fixture $result; throw 'Changed digest was accepted.' }
        catch { if ($_.Exception.Message -notlike '*size or SHA-256 differs*') { throw } }
        [IO.File]::WriteAllBytes($fixture, [byte[]](11,23))
        try { Assert-WindowsPackage $fixture $result; throw 'Changed size was accepted.' }
        catch { if ($_.Exception.Message -notlike '*size or SHA-256 differs*') { throw } }
    }
    if ($PublishedMatrix) {
        $published = Get-Content -Raw -LiteralPath $PublishedMatrix | ConvertFrom-Json
        & {
            function Get-WindowsReleaseCandidates { [pscustomobject]@{Tag="v$($published.build_version)";Version=$published.build_version;FromFeed=$false} }
            function Get-WindowsReleaseText([string]$Uri) { Get-Content -Raw -LiteralPath $PublishedMatrix }
            foreach ($platform in @('x64','arm64')) {
                foreach ($edition in @('client','full')) {
                    $asset=Resolve-WindowsReleasePackage $edition $platform
                    if ($PublishedArchive -and $platform -eq 'x64' -and $edition -eq 'full') { Assert-WindowsPackage $PublishedArchive $asset }
                    Write-Host "Published manifest selected $($asset.Tag) $platform $edition $($asset.Size) bytes."
                }
            }
        }
    }
    if ($LiveMetadata) {
        $real = Resolve-WindowsReleasePackage 'client' $arch
        Write-Host "Public resolver selected $($real.Tag) $($real.Url) $($real.Size) $($real.Sha256)."
    }
    Write-Host 'Windows release resolver verified numeric versions, stable releases, role, native architecture, HTTP fallback, size, and SHA-256.'
} finally { Remove-Item -Force -LiteralPath $fixture -ErrorAction SilentlyContinue }
