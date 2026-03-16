import type { Metadata } from "next";
import { DM_Mono, IBM_Plex_Mono, Instrument_Sans } from "next/font/google";
import "./globals.css";

const dmMono = DM_Mono({
  weight: ["400", "500"],
  subsets: ["latin"],
  variable: "--font-dm-mono",
});

const ibmPlexMono = IBM_Plex_Mono({
  weight: ["400", "500", "600"],
  subsets: ["latin"],
  variable: "--font-ibm-plex-mono",
});

const instrumentSans = Instrument_Sans({
  weight: ["400", "500", "600", "700"],
  subsets: ["latin"],
  variable: "--font-instrument-sans",
});

export const metadata: Metadata = {
  title: "Rebuno Explorer",
  description: "Rebuno explorer",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className={`dark ${dmMono.variable} ${ibmPlexMono.variable} ${instrumentSans.variable}`}>
      <body className="min-h-screen noise-texture">
        {children}
        <div className="scanline-overlay" />
      </body>
    </html>
  );
}
