// Barrel for the Moyro HTTP API.
//
// The concrete request builders live in the modules re-exported below; this
// file exists so the ~80 call sites that import from `@/api/client` keep a
// single, stable import path. Add new endpoints to the module that owns the
// surface, not here.
export * from "./media";
export * from "./chat";
export * from "./integrations";
export * from "./compat";
export * from "./moyro";
