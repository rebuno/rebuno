/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./app/**/*.{js,ts,jsx,tsx}",
    "./components/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        surface: {
          0: "#0c0c0e",
          1: "#141416",
          2: "#1c1c20",
          3: "#26262c",
        },
        border: {
          DEFAULT: "#2e2e36",
          hover: "#44444f",
        },
        accent: {
          DEFAULT: "#3B82F6",
          hover: "#60A5FA",
          muted: "rgba(59, 130, 246, 0.12)",
          dim: "rgba(59, 130, 246, 0.06)",
        },
        phosphor: {
          green: "#3ddc84",
          red: "#f44336",
          amber: "#e8a832",
          blue: "#3B82F6",
          cyan: "#4dd0e1",
          purple: "#b388ff",
        },
      },
      fontFamily: {
        display: ['var(--font-instrument-sans)', 'system-ui', 'sans-serif'],
        body: ['var(--font-instrument-sans)', 'system-ui', 'sans-serif'],
        mono: ['var(--font-ibm-plex-mono)', 'var(--font-dm-mono)', 'monospace'],
      },
      animation: {
        "pulse-slow": "pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite",
        "fade-in": "fadeIn 0.25s ease-out",
        "slide-up": "slideUp 0.2s ease-out",
      },
      keyframes: {
        fadeIn: {
          "0%": { opacity: "0" },
          "100%": { opacity: "1" },
        },
        slideUp: {
          "0%": { opacity: "0", transform: "translateY(6px)" },
          "100%": { opacity: "1", transform: "translateY(0)" },
        },
      },
    },
  },
  plugins: [],
};
