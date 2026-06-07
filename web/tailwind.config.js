/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      fontFamily: {
        headline: ["Geist", "sans-serif"],
        body: ["Inter", "sans-serif"],
        label: ["Inter", "sans-serif"],
      },
      colors: {
        primary: "#09090b",
        "on-primary": "#ffffff",
        "border-subtle": "rgba(0, 0, 0, 0.04)",
        "surface-container-low": "#f4f4f5",
        "surface-container-high": "#e4e4e7",
        "surface-container-lowest": "#fafafa",
        "on-surface-variant": "#71717a",
        accent: {
          gold: "#f59e0b",
          elixir: "#d946ef",
          dark: "#18181b",
        }
      },
      boxShadow: {
        'premium': '0 4px 20px -2px rgba(0, 0, 0, 0.05), 0 2px 10px -2px rgba(0, 0, 0, 0.03)',
        'premium-hover': '0 10px 30px -4px rgba(0, 0, 0, 0.08), 0 4px 15px -4px rgba(0, 0, 0, 0.04)',
        'premium-lg': '0 20px 50px -12px rgba(0, 0, 0, 0.1)',
      }
    },
  },
  plugins: [],
}


