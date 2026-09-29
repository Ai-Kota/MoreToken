' run-mt-deploy-hidden.vbs - run daily mt-deploy with NO console window (T-028).
' Mirrors the proven dev-fleet run-hidden.vbs pattern (window style 0 = hidden).
' Scheduled task: MoreToken-DailyRedeploy (daily 12:37) -> wscript //B this file.
' NOTE: keep this file ASCII-only - WSH parses .vbs as ANSI, non-ASCII breaks it.
' NOTE: deploy output is logged by mt-deploy-logged.sh to docker/runtime/deploy.log.
Set sh = CreateObject("WScript.Shell")
sh.CurrentDirectory = "E:\AImlyForge\tools\agent\moretoken"
sh.Run """D:\Git\usr\bin\bash.exe"" docker/mt-deploy-logged.sh", 0, False
