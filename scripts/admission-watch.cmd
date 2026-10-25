@echo off
:: ============================================================================
:: admission-watch.cmd -- MoreToken-owned admission pipeline (T-049).
::
:: Owned by the moretoken repo, NOT dev-fleet (user is retiring dev-fleet).
::   step 1: TEST/model-evaluator produces a fresh admitted.json (raw evidence)
::   step 2: atomically copy it to docker/runtime/admitted.json
::   step 3: the gateway hot-reloads and merges it with the previous table
::           (best-of-window 3h, see internal/admission) -- no restart, no
::           merge logic on the host anymore.
::
:: Run by scheduled task MoreToken-AdmissionSync (every 30 min) through
:: run-admission-hidden.vbs (no console window).
::
:: ASCII ONLY -- cmd.exe parses this in the OEM codepage.
setlocal
set "EV=E:\AImlyForge\TEST\model-evaluator"
set "TARGET=E:\AImlyForge\tools\agent\moretoken\docker\runtime\admitted.json"
set "LOG=E:\AImlyForge\tools\agent\moretoken\docker\runtime\admission-sync.log"

call :log "[admission-watch] %date% %time% round start"
:: Freshness guard: move the previous artifact away first. If this round is
:: killed / fails before writing a new admitted.json, we must NOT copy stale data.
if exist "%EV%\admitted.json" move /Y "%EV%\admitted.json" "%EV%\admitted.json.prev" >nul
"%EV%\bin\model-evaluator.exe" watch --sources "%EV%\sources.direct.json" --out "%EV%" --limit 12 --delay 3s --once >>"%LOG%" 2>&1
set "WC=%ERRORLEVEL%"
call :log "[admission-watch] watch exit=%WC%"
if %WC% LSS 0 (
  call :log "[admission-watch] watch killed/abnormal (%WC%), skip copy (keep old table)"
  exit /b 1
)
if %WC% GEQ 2 (
  call :log "[admission-watch] watch hard-failed, skip copy"
  exit /b %WC%
)

if not exist "%EV%\admitted.json" (
  call :log "[admission-watch] no fresh evaluator output this round, skip copy (keep old table)"
  exit /b 1
)
for %%A in ("%EV%\admitted.json") do if %%~zA LEQ 2 (
  call :log "[admission-watch] empty evaluator output, skip copy (keep old table)"
  exit /b 1
)

copy /Y "%EV%\admitted.json" "%TARGET%.new" >nul
if errorlevel 1 (
  call :log "[admission-watch] copy failed, keep old table"
  exit /b 1
)
move /Y "%TARGET%.new" "%TARGET%" >nul
if errorlevel 1 (
  call :log "[admission-watch] atomic replace failed, keep old table"
  exit /b 1
)
call :log "[admission-watch] round done (gateway will merge on mtime change)"
exit /b 0

:log
echo %~1 >> "%LOG%"
exit /b 0
