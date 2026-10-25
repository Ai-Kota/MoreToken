' run-admission-hidden.vbs -- run the MoreToken admission pipeline with no console.
' Scheduled task: MoreToken-AdmissionSync (every 30 min) -> wscript //B this file.
' Owned by moretoken, NOT dev-fleet. Keep ASCII-only (WSH parses .vbs as ANSI).
Set sh = CreateObject("WScript.Shell")
sh.CurrentDirectory = "E:\AImlyForge\tools\agent\moretoken"
' wait=True: keep the scheduled task "running" for the whole evaluator+copy round,
' so the next 30-min tick does not start a second evaluator over the same keys.
sh.Run "cmd.exe /c E:\AImlyForge\tools\agent\moretoken\scripts\admission-watch.cmd", 0, True
