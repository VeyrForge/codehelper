function Write-Info {
    param(
        [string]$Message
    )
    Write-Host $Message
    Deploy-App -Target $Message
}

function Deploy-App {
    param(
        [string]$Target
    )
    Prepare-Env
    # Get-ChildItem is filtered cmdlet noise
    Get-ChildItem .
}

function Prepare-Env {
    $env:DEPLOY_READY = "1"
}
