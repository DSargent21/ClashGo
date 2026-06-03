/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        primary: "#09090b",
        "on-primary": "#ffffff",
        "ethereal-cyan": "#00F2FE",
        "ethereal-violet": "#BF5AF2",
        "ethereal-blue": "#0A84FF",
        "border-subtle": "rgba(0, 0, 0, 0.08)",
        "surface-container-low": "#f4f4f5",
        "surface-container-high": "#e4e4e7",
        "surface-container-lowest": "#fafafa",
        "on-surface-variant": "#71717a",
      },
    },
  },
  plugins: [],
}

