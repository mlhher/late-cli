package tui

import (
	"charm.land/lipgloss/v2"
)

var (
	// Refined Palette - Pure Obsidian, Warm Golden Amber, Calm Steel Ice
	primaryColor   = lipgloss.Color("#E5A85C") // Warm Golden Amber
	primaryGlow    = lipgloss.Color("#F0B875") // Subtle highlight
	secondaryColor = lipgloss.Color("#56B6C2") // Steel Ice Cyan
	accentEmerald  = lipgloss.Color("#4ECCA3") // Muted Mint
	accentPurple   = lipgloss.Color("#9D86E9") // Muted Violet
	accentCoral    = lipgloss.Color("#E06C75") // Calm Coral
	warningColor   = lipgloss.Color("#E06C75") // Warning Red (alias)
	textColor      = lipgloss.Color("#E6EDF3") // Crisp Off-White
	subtextColor   = lipgloss.Color("#7D8590") // Muted Slate
	mutedTextColor = lipgloss.Color("#484F58") // Whisper Slate (borders, dividers)

	// Canvas & Surfaces
	appBgColor     = lipgloss.Color("#0B0C0E") // Deep Obsidian
	userMsgBg      = appBgColor                // Clean seamless canvas
	aiMsgBg        = appBgColor                // Clean seamless canvas
	thoughtBgColor = lipgloss.Color("#0E1015") // Recessed Introspection
	cardBgColor    = lipgloss.Color("#12141A") // Subtle Container (autocomplete popup)
	chipBgColor    = lipgloss.Color("#1B1E28") // Elevated Pill / Badge Surface
	borderColor    = lipgloss.Color("#1C1F26") // Hairline Divider
	activeBorder   = lipgloss.Color("#2D323E") // Focused / Active Border

	// Centralized Border Styles & Colors
	boxBorderStyle   = lipgloss.RoundedBorder()
	modalBorderColor = secondaryColor // Primary border color for dialogs & overlays (/help, commit detail)
	cardBorderColor  = borderColor    // Subtle border for embedded cards & matrices (welcome card)
	warnBorderColor  = accentCoral    // Warning / confirmation border color (stop dialog, tool permissions)
	errorBorderColor = accentCoral    // Error border color (context limit exceeded)

	modalBoxStyle = lipgloss.NewStyle().
			MarginLeft(1).
			Padding(1, 2).
			Border(boxBorderStyle).
			BorderForeground(modalBorderColor).
			Background(appBgColor)

	// Centralized Pill & Badge Styles (Elevated Neutral Surface)
	chipStyle = lipgloss.NewStyle().
			Background(chipBgColor).
			Padding(0, 1)

	commitHashChipStyle     = chipStyle.Foreground(secondaryColor)
	commitSelectedChipStyle = chipStyle.Foreground(primaryColor).Bold(true)
	modelPickerChipStyle    = chipStyle.Foreground(subtextColor)
	headBadgeStyle          = lipgloss.NewStyle().
					Foreground(appBgColor).
					Background(accentEmerald).
					Bold(true).
					Padding(0, 1)

	telemetryChipStyle  = chipStyle
	telemetryLabelStyle = lipgloss.NewStyle().
				Foreground(subtextColor).
				Background(chipBgColor)
	telemetryValueStyle = lipgloss.NewStyle().
				Foreground(textColor).
				Background(chipBgColor)

	// Base Style for inheritance
	baseStyle = lipgloss.NewStyle().Background(appBgColor)

	// Layout Constants
	UserMsgOverhead = 0
	AIMsgOverhead   = 0

	// Styles
	appStyle = baseStyle.Copy().
			Foreground(textColor)

	inputStyle = baseStyle.Copy().
			BorderBackground(appBgColor).
			MarginBackground(appBgColor).
			Padding(0, 1)

	// User Prompt & Message
	promptSymbolStyle = lipgloss.NewStyle().
				Foreground(primaryColor).
				Bold(true)

	userMsgStyle = lipgloss.NewStyle().
			Foreground(textColor).
			Background(appBgColor).
			Align(lipgloss.Left)

	queuedMsgStyle = userMsgStyle.Copy().
			Foreground(subtextColor)

	attachmentStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Italic(true)

	// AI Response
	aiMsgStyle = baseStyle.Copy().
			Foreground(textColor).
			Background(appBgColor)

	// Thinking / Introspection Gutter
	thinkingStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			Italic(true).
			Padding(0, 1).
			MarginLeft(2).
			BorderLeft(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(activeBorder).
			BorderBackground(appBgColor)

	tagStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			Background(appBgColor).
			MarginLeft(2)

	thoughtHeaderStyle = lipgloss.NewStyle().
				Foreground(subtextColor).
				Italic(true).
				Background(appBgColor).
				MarginLeft(2)

	// Tool execution
	toolBadgeStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			MarginLeft(2)

	// Status Bar
	statusBarBaseStyle = lipgloss.NewStyle().
				Background(appBgColor).
				MarginBackground(appBgColor).
				Border(lipgloss.NormalBorder(), true, false, false, false).
				BorderForeground(borderColor).
				BorderBackground(appBgColor).
				Foreground(textColor)

	statusDivider = lipgloss.NewStyle().
			Foreground(activeBorder).
			Background(appBgColor).
			Render(" │ ")

	statusModeStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			Bold(true)

	statusTextStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor)

	statusWarningStyle = lipgloss.NewStyle().
				Foreground(accentCoral).
				Background(appBgColor).
				Bold(true)

	statusSuccessStyle = lipgloss.NewStyle().
				Foreground(accentEmerald).
				Background(appBgColor)

	keycapStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(chipBgColor).
			Padding(0, 1)

	statusAttachedStyle = lipgloss.NewStyle().
				Foreground(secondaryColor).
				Background(appBgColor)

	statusTokenStyle = lipgloss.NewStyle().
				Foreground(subtextColor)

	// Breadcrumb styles
	breadcrumbLateStyle = lipgloss.NewStyle().
				Foreground(subtextColor).
				Background(appBgColor)

	breadcrumbSeparatorStyle = lipgloss.NewStyle().
					Foreground(activeBorder).
					Background(appBgColor)

	breadcrumbAgentStyle = lipgloss.NewStyle().
				Foreground(textColor).
				Background(appBgColor)
)
