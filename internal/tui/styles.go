package tui

import (
	"charm.land/lipgloss/v2"
)

var (
	// Premium Palette - Deep Dark / Obsidian
	primaryColor   = lipgloss.Color("#9B59B6") // Amethyst
	secondaryColor = lipgloss.Color("#2ECC71") // Emerald
	textColor      = lipgloss.Color("#ECF0F1") // Clouds
	subtextColor   = lipgloss.Color("#95A5A6") // Concrete
	warningColor   = lipgloss.Color("#F1C40F") // Sunflower/Yellow

	// Message Backgrounds
	appBgColor     = lipgloss.Color("#191919")
	userMsgBg      = lipgloss.Color("#16222A") // Very dark blue/black
	aiMsgBg        = appBgColor                // Keep alias for AI msgs
	thoughtBgColor = lipgloss.Color("#101010") // Near black

	// Base Style for inheritance
	baseStyle = lipgloss.NewStyle().Background(appBgColor)

	// Layout Constants
	UserMsgOverhead = 6 // MarginL(1) + Border(1) + Padding(2)*2 = 6
	AIMsgOverhead   = 8 // MarginL(1) + Border(1) + PaddingL(4) + PaddingR(2) = 8

	// Styles
	appStyle = baseStyle.Copy().
			Foreground(textColor)

	inputStyle = baseStyle.Copy().
			Border(lipgloss.NormalBorder(), true, false, false, false).
			BorderForeground(lipgloss.Color("#252525")).
			BorderBackground(appBgColor).
			MarginBackground(appBgColor).
			Padding(0, 1).
			Height(InputHeight - 1)

	// User Bubble
	userMsgStyle = lipgloss.NewStyle().
			Background(userMsgBg).
			Foreground(textColor).
			Padding(0, 2).
			MarginLeft(1).
			MarginBackground(appBgColor).
			Align(lipgloss.Left).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderLeftForeground(secondaryColor).
			BorderBackground(userMsgBg).
			PaddingLeft(2)

	queuedMsgStyle = userMsgStyle.Copy().
			Foreground(subtextColor).
			BorderLeftForeground(subtextColor)

	attachmentStyle = lipgloss.NewStyle().
			Foreground(secondaryColor).
			Italic(true)

	// AI Bubble
	aiMsgStyle = baseStyle.Copy().
			Padding(0, 2).
			MarginLeft(1).
			MarginBackground(appBgColor).
			PaddingLeft(4).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderLeftForeground(primaryColor).
			BorderBackground(appBgColor)

	// Thinking Block
	thinkingStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(thoughtBgColor).
			Italic(true).
			Padding(0, 1).
			MarginLeft(4).
			BorderLeft(true).
			BorderStyle(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("#555555")).
			BorderBackground(thoughtBgColor)

	tagStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			Background(thoughtBgColor).
			MarginBackground(appBgColor).
			MarginLeft(1).
			PaddingLeft(1)

	thoughtHeaderStyle = tagStyle.Copy().
				Foreground(subtextColor)

	statusBarBaseStyle = lipgloss.NewStyle().
				Background(appBgColor).
				MarginBackground(appBgColor).
				Border(lipgloss.NormalBorder(), true, false, false, false).
				BorderForeground(lipgloss.Color("#444444")).
				BorderBackground(appBgColor).
				Foreground(textColor)

	statusModeStyle = lipgloss.NewStyle().
			Background(primaryColor).
			Foreground(textColor).
			Padding(0, 1).
			Bold(true).
			MarginRight(1).
			MarginBackground(appBgColor)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Background(appBgColor).
			MarginBackground(appBgColor).
			Bold(true)

	statusTextStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(appBgColor).
			MarginBackground(appBgColor).
			MarginLeft(1)

	statusWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#121212")).
				Background(warningColor).
				MarginBackground(appBgColor).
				Bold(true).
				Padding(0, 1).
				MarginLeft(1)
)
