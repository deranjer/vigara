import { createTheme, type CSSVariablesResolver, type MantineColorsTuple } from '@mantine/core'

// Vigara visual system (agreed in the redesign review, Nov 2026).
//
// Shade 7 is the light-scheme colour and shades 2–4 are the dark-scheme colours, because Mantine
// derives each scheme's variants from those positions when primaryShade is { light: 7, dark: 7 }:
// light scheme: filled, light-variant text and outline all use shade 7;
// dark scheme: filled uses 7, light-variant text uses 2, outline 3 and links 4.
const blue: MantineColorsTuple = ['#EAF2FC', '#D4E4F8', '#8ABAF2', '#6AA6EE', '#4A8AD9', '#3778C8', '#2A6ABC', '#1D5FB0', '#174E92', '#103C72']
const green: MantineColorsTuple = ['#E6F6EE', '#C9EBDA', '#6FD3A2', '#43C285', '#2FA86F', '#249560', '#1D8A57', '#17804F', '#116641', '#0B4C30']
const red: MantineColorsTuple = ['#FDECEC', '#F9D2D3', '#F58E91', '#F0686C', '#E4545A', '#D9454A', '#D03C41', '#C9353A', '#A72A2F', '#821F23']
// Warnings are amber. Brand orange (#FF8136) is never a status colour, so "orange" maps to amber too.
const amber: MantineColorsTuple = ['#FFF6E5', '#FDEACB', '#F5BD66', '#F0A73A', '#DE9019', '#CF7F08', '#C27403', '#B76A00', '#965700', '#714100']
// Neutrals carry a slight navy bias from the brand.
const gray: MantineColorsTuple = ['#F5F7FA', '#EEF2F6', '#E4E9F0', '#DCE2EA', '#C5CED9', '#9AA5B4', '#6A7587', '#475467', '#2A3646', '#0E1A29']
// Dark scheme surfaces: 7 = cards and body, 6 = inputs, 5 = hover, 4 = borders, 0 = text, 2 = dimmed text.
const dark: MantineColorsTuple = ['#E7EDF4', '#C9D3DF', '#A7B4C4', '#7F8C9E', '#2E4361', '#22334A', '#1C2D42', '#101B28', '#0A121C', '#07101A']
const navy: MantineColorsTuple = ['#E2E9F1', '#C3D1E0', '#9DBBDA', '#7194BA', '#4D7398', '#335878', '#24425F', '#1A3A5A', '#122D47', '#07101A']

export const theme = createTheme({
  primaryColor: 'blue',
  primaryShade: { light: 7, dark: 7 },
  colors: { blue, green, red, yellow: amber, orange: amber, gray, dark, navy },
  fontFamily: '"Inter", ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif',
  fontFamilyMonospace: '"JetBrains Mono", ui-monospace, "SFMono-Regular", Consolas, monospace',
  headings: {
    fontFamily: '"Inter", ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif',
    fontWeight: '700',
    sizes: {
      h1: { fontSize: '28px', lineHeight: '1.2' },
      h2: { fontSize: '20px', lineHeight: '1.3' },
      h3: { fontSize: '16px', lineHeight: '1.35' },
      h4: { fontSize: '14px', lineHeight: '1.4' },
    },
  },
  // Type scale: 12 label · 13 dense · 14 body · 16 section · 20 page.
  fontSizes: { xs: '12px', sm: '13px', md: '14px', lg: '16px', xl: '20px' },
  // Soft corners: 6 px controls, 10 px panels.
  radius: { xs: '4px', sm: '6px', md: '10px', lg: '12px', xl: '16px' },
  defaultRadius: 'sm',
  components: {
    Card: { defaultProps: { radius: 'md' } },
    Paper: { defaultProps: { radius: 'md' } },
    Modal: { defaultProps: { radius: 'md' } },
  },
})

// Tokens that Mantine has no theme slot for: the page canvas behind cards, the navy sidebar and
// status colours for the dot + word status style.
export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {
    '--vg-brand': '#FF8136',
    '--vg-rail': '#122D47',
    '--vg-rail-2': '#1A3A5A',
    '--vg-rail-text': '#C9D6E4',
    '--vg-rail-dim': '#86A0BB',
  },
  light: {
    '--mantine-color-text': '#0E1A29',
    '--vg-canvas': '#F5F7FA',
    '--vg-ok': '#17804F',
    '--vg-warn': '#B76A00',
    '--vg-crit': '#C9353A',
    '--vg-info': '#1D5FB0',
    '--vg-idle': '#8893A2',
  },
  dark: {
    '--vg-canvas': '#0A121C',
    '--vg-rail': '#07101A',
    '--vg-rail-2': '#13253A',
    '--vg-rail-dim': '#7891AC',
    '--vg-ok': '#43C285',
    '--vg-warn': '#F0A73A',
    '--vg-crit': '#F0686C',
    '--vg-info': '#6AA6EE',
    '--vg-idle': '#6F7C8D',
  },
})
