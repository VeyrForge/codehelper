import { NextResponse } from "next/server";
import { greet } from "../../../lib/greet";

export async function GET() {
  return NextResponse.json({ message: greet("api") });
}
