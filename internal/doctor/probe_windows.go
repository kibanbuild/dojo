package doctor

import (
	"context"
	"os/exec"
)

// Each fact prints exactly one line, empty when unknown. Firmware
// virtualization reads False once a hypervisor (Hyper-V) is running, so a
// present hypervisor also counts as enabled.
const probeScript = `$c = Get-CimInstance Win32_ComputerSystem
$p = Get-CimInstance Win32_Processor | Select-Object -First 1
Write-Output "$($c.TotalPhysicalMemory)"
Write-Output "$($c.HypervisorPresent -or $p.VirtualizationFirmwareEnabled)"
Write-Output "$((Get-Item $env:USERPROFILE).PSDrive.Free)"`

func hostFacts(ctx context.Context, m *Machine) {
	out, err := runCmd(ctx, func(c context.Context) *exec.Cmd {
		return exec.CommandContext(c, "powershell", "-NoProfile", "-NonInteractive", "-Command", probeScript)
	})
	if err != nil {
		return
	}
	parseWindowsFacts(out, m)
}
