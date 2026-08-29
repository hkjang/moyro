import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: [["line"]],
  outputDir: "./test-results",
  use: {
    baseURL: process.env.MOYRO_BASE_URL ?? "http://127.0.0.1:8065",
    locale: "ko-KR",
    timezoneId: "Asia/Seoul",
    colorScheme: "light",
    serviceWorkers: "block",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 1000 },
      },
    },
  ],
});
