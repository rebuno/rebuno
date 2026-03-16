import { NextRequest, NextResponse } from "next/server";

const KERNEL_URL = process.env.KERNEL_URL || "http://localhost:8080";
const KERNEL_API_KEY = process.env.KERNEL_API_KEY || "";

const FORWARDED_HEADERS = ["Content-Type"] as const;

async function proxy(req: NextRequest) {
  const url = new URL(req.url);

  if (!url.pathname.startsWith("/v0/") || url.pathname.includes("..")) {
    return NextResponse.json({ error: "Invalid path" }, { status: 400 });
  }

  const target = `${KERNEL_URL}${url.pathname}${url.search}`;

  const headers: Record<string, string> = {};
  for (const name of FORWARDED_HEADERS) {
    const value = req.headers.get(name);
    if (value) headers[name] = value;
  }

  if (KERNEL_API_KEY) {
    headers["Authorization"] = `Bearer ${KERNEL_API_KEY}`;
  }

  const init: RequestInit = { method: req.method, headers };

  if (req.method !== "GET" && req.method !== "HEAD") {
    const body = await req.text();
    if (body) init.body = body;
  }

  try {
    const resp = await fetch(target, init);
    const data = await resp.arrayBuffer();
    return new NextResponse(data, {
      status: resp.status,
      headers: {
        "Content-Type": resp.headers.get("Content-Type") || "application/json",
      },
    });
  } catch (err) {
    console.error(`Failed to proxy ${target}`, err);
    return NextResponse.json(
      { error: "Kernel unavailable" },
      { status: 502 }
    );
  }
}

export const GET = proxy;
export const POST = proxy;
export const PUT = proxy;
export const DELETE = proxy;
export const PATCH = proxy;
