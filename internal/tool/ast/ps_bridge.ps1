# ps_bridge.ps1 — AST-backed command parser for the late agent.
# Runs as a persistent process. Each iteration reads one newline-delimited JSON
# request from stdin ({"cmd":"..."}), parses the command with
# System.Management.Automation.Language.Parser::ParseInput, walks the
# resulting AST, and writes one compact JSON IR line to stdout.
# Repeats until stdin is closed (EOF), then exits.
#
# CONTRACT:
#   - This script NEVER executes the input command.
#   - Each response is exactly one line of compact JSON matching ParsedIR schema.
#   - On any error the script still emits valid JSON with risk_flag "syntax_error".
#   - Exit code is always 0 (errors are encoded in the JSON payload).
#   - Each response line is flushed immediately so the Go caller can read it.

param()

Set-StrictMode -Off
$ErrorActionPreference = 'Stop'

function New-IR {
    return @{
        version      = "1"
        platform     = "windows"
        commands     = [System.Collections.Generic.List[string]]::new()
        operators    = [System.Collections.Generic.List[string]]::new()
        redirects    = [System.Collections.Generic.List[string]]::new()
        expansions   = [System.Collections.Generic.List[string]]::new()
        risk_flags   = [System.Collections.Generic.List[string]]::new()
        parse_errors = [System.Collections.Generic.List[string]]::new()
        command_args = @{}  # hashtable: cmdName -> List[string] of flags
    }
}

function Add-Unique {
    param([System.Collections.Generic.List[string]]$List, [string]$Value)
    if (-not $List.Contains($Value)) { $List.Add($Value) | Out-Null }
}

# Dynamic-evaluation cmdlets — emit "invoke_expression".
# These can hide execution intent or run arbitrary code.
$invokeRiskCmdlets = @(
    'invoke-expression', 'iex',
    'start-process',     'saps',
    'invoke-command',    'icm',
    'new-object'
)

# Destructive filesystem cmdlets — emit "destructive".
# These modify or remove files and require user confirmation, but are
# semantically distinct from dynamic-evaluation risks.
$destructiveCmdlets = @(
    'remove-item',       'ri', 'del', 'erase', 'rd', 'rmdir', 'rm',
    'rename-item',       'rni', 'ren',
    'move-item',         'mi', 'move', 'mv',
    'copy-item',         'ci', 'copy', 'cp',
    'set-content',       'sc',        # NOTE: 'sc' also matches sc.exe (Service Control Manager); accepted false-positive.
    'add-content',       'ac',
    'out-file',
    'clear-content',     'clc',
    'set-itemproperty',  'sp',
    'set-acl'
)

# cd / Set-Location aliases — policy engine blocks these.
$cdCmdlets = @(
    'set-location', 'sl', 'cd', 'chdir',
    'push-location', 'pushd',
    'pop-location',  'popd'
)

# Path-creation cmdlets (used for the new-path carveout signal).
$newPathCmdlets = @('mkdir', 'md', 'new-item', 'ni')

# Invoke-Parse parses one command string and returns an IR hashtable.
# Never throws — all errors are encoded inside the returned IR.
function Invoke-Parse {
    param([string]$command)

    $ir = New-IR

    # --- Parse ---
    $tokens = $null
    try {
        $tokenArr = [System.Management.Automation.Language.Token[]]@()
        $errorArr = [System.Management.Automation.Language.ParseError[]]@()
        $ast = [System.Management.Automation.Language.Parser]::ParseInput(
            $command, [ref]$tokenArr, [ref]$errorArr
        )
        $tokens      = $tokenArr
        $parseErrors = $errorArr
    } catch {
        Add-Unique $ir.risk_flags "syntax_error"
        $ir.parse_errors.Add($_.ToString()) | Out-Null
        return $ir
    }

    # Record parser diagnostics (soft errors — PS parser is lenient).
    foreach ($e in $parseErrors) {
        $ir.parse_errors.Add($e.Message) | Out-Null
        Add-Unique $ir.risk_flags "syntax_error"
    }

    # --- Walk every AST node ---
    $astNodes = $ast.FindAll({ $true }, $true)
    foreach ($node in $astNodes) {
        # Commands
        if ($node -is [System.Management.Automation.Language.CommandAst]) {
            $elems = $node.CommandElements
            if ($elems -and $elems.Count -gt 0) {
                $cmdName = $elems[0].ToString().Trim().ToLower()
                if ($cmdName -ne '') {
                    # For tier2 commands (git, go), detect subcommand to form compound key.
                    # This ensures "git log" and "git push" are stored as separate allow-list entries.
                    $cmdKey = $cmdName
                    $argsStartIdx = 1
                    if (($cmdName -eq "git" -or $cmdName -eq "go") -and $elems.Count -gt 1) {
                        $subCmd = $elems[1].ToString().Trim().ToLower()
                        # Check if the next element is NOT a parameter flag (doesn't start with -)
                        if ($subCmd -and -not $subCmd.StartsWith('-')) {
                            $cmdKey = $cmdName + " " + $subCmd
                            $argsStartIdx = 2
                        }
                    }
                    
                    Add-Unique $ir.commands $cmdKey
                    if ($invokeRiskCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "invoke_expression"
                    }
                    if ($destructiveCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "destructive"
                    }
                    if ($cdCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "cd"
                    }
                    if ($newPathCmdlets -contains $cmdName) {
                        Add-Unique $ir.risk_flags "new_path"
                    }
                    # Collect flags for policy engine allow-list matching.
                    if (-not $ir.command_args.ContainsKey($cmdKey)) {
                        $ir.command_args[$cmdKey] = [System.Collections.Generic.List[string]]::new()
                    }
                    for ($i = $argsStartIdx; $i -lt $elems.Count; $i++) {
                        $argText = $elems[$i].ToString().Trim()
                        if ($argText.StartsWith('-')) {
                            # Normalize -Param:value → -Param (PowerShell colon syntax)
                            $colonIdx = $argText.IndexOf(':')
                            if ($colonIdx -gt 0) { $argText = $argText.Substring(0, $colonIdx) }
                            if (-not $ir.command_args[$cmdKey].Contains($argText)) {
                                $ir.command_args[$cmdKey].Add($argText) | Out-Null
                            }
                        }
                    }
                }
            }
            continue
        }

        # Pipeline: | operator
        if ($node -is [System.Management.Automation.Language.PipelineAst]) {
            if ($node.PipelineElements.Count -gt 1) {
                Add-Unique $ir.operators "|"
                Add-Unique $ir.risk_flags "operator"
            }
            continue
        }

        # && and || via PipelineChain (PS7+). Guard with -is check so older PS
        # versions skip gracefully (the type simply won't exist).
        if ($node.GetType().Name -eq 'PipelineChainAst') {
            try {
                $op = $node.Operator.ToString()
                Add-Unique $ir.operators $op
                Add-Unique $ir.risk_flags "operator"
            } catch {}
            continue
        }

        # File redirection (> >>)
        if ($node -is [System.Management.Automation.Language.FileRedirectionAst]) {
            Add-Unique $ir.redirects "FileRedirection"
            Add-Unique $ir.risk_flags "redirect"
            continue
        }

        # Merging redirection (2>&1 etc.) — not inherently risky, just record it.
        if ($node -is [System.Management.Automation.Language.MergingRedirectionAst]) {
            Add-Unique $ir.redirects "MergingRedirection"
            continue
        }

        # $(...) sub-expression
        if ($node -is [System.Management.Automation.Language.SubExpressionAst]) {
            Add-Unique $ir.expansions "subshell"
            Add-Unique $ir.risk_flags "subshell"
            continue
        }

        # Variable: $var — flag genuine dynamic expansions only.
        # Exclude PowerShell language constants ($true, $false, $null) and the
        # pipeline iteration variable ($_ / $PSItem): they are not user-controlled
        # and trigger false-positive confirmations on normal filter expressions
        # such as  Where-Object { $_.Name -eq 'foo' }.
        if ($node -is [System.Management.Automation.Language.VariableExpressionAst]) {
            $varName = $node.VariablePath.UserPath.ToLower()
            $constants = @('true', 'false', 'null', '_', 'psitem')
            if ($constants -notcontains $varName) {
                Add-Unique $ir.expansions "var"
                Add-Unique $ir.risk_flags "expansion"
            }
            continue
        }

    }

    # Detect ';' statement separator from top-level statement count.
    try {
        $endBlock = $ast.EndBlock
        if ($endBlock -and $endBlock.Statements -and $endBlock.Statements.Count -gt 1) {
            Add-Unique $ir.operators ";"
            Add-Unique $ir.risk_flags "operator"
        }
    } catch {}

    # Scan tokens for -EncodedCommand / -enc flags and &&/|| (PS5.1 fallback
    # since PipelineChainAst only exists in PS7+).
    foreach ($tok in $tokens) {
        $tv = $tok.Text.ToLower()
        switch ($tv) {
            '-encodedcommand' { Add-Unique $ir.risk_flags "invoke_expression" }
            '-enc'            { Add-Unique $ir.risk_flags "invoke_expression" }
            '-en'             { Add-Unique $ir.risk_flags "invoke_expression" }
            '&&' {
                Add-Unique $ir.operators "&&"
                Add-Unique $ir.risk_flags "operator"
            }
            '||' {
                Add-Unique $ir.operators "||"
                Add-Unique $ir.risk_flags "operator"
            }
        }
    }

    return $ir
}

# Write-IR serializes an IR hashtable to a compact JSON line and flushes stdout
# immediately so the Go reader is not left waiting for a buffer to fill.
function Write-IR {
    param($ir)
    # Convert command_args hashtable → plain hashtable of arrays for JSON.
    $cmdArgsOut = @{}
    foreach ($k in $ir.command_args.Keys) {
        $cmdArgsOut[$k] = @($ir.command_args[$k])
    }
    $out = [ordered]@{
        version      = $ir.version
        platform     = $ir.platform
        commands     = @($ir.commands)
        operators    = @($ir.operators)
        redirects    = @($ir.redirects)
        expansions   = @($ir.expansions)
        risk_flags   = @($ir.risk_flags)
        parse_errors = @($ir.parse_errors)
        command_args = $cmdArgsOut
    }
    [Console]::Out.WriteLine(($out | ConvertTo-Json -Compress -Depth 3))
    [Console]::Out.Flush()
}

# --- Main persistent loop ---
# Each iteration: read one JSON request line {"cmd":"..."}, parse, emit one
# JSON response line. Exits when stdin reaches EOF (Go closes the pipe).
while ($true) {
    $line = $null
    try {
        $line = [Console]::In.ReadLine()
    } catch {
        break
    }
    if ($null -eq $line) { break }
    $line = $line.Trim()
    if ($line -eq '') { continue }

    $ir = $null
    try {
        $req = ConvertFrom-Json $line
        $ir = Invoke-Parse $req.cmd
    } catch {
        $ir = New-IR
        Add-Unique $ir.risk_flags "syntax_error"
        $ir.parse_errors.Add("request error: $_") | Out-Null
    }

    Write-IR $ir
}
