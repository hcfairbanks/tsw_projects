# BR 442 (Talent 2) German safety-system API endpoints

Captured live from `RVM_DLG_DB_BR442_T1_C` on **Bahnstrecke Leipzig–Dresden**, train stationary
(so all warning flags read `false`, but the **value shapes** below are authoritative).

Base: `http://127.0.0.1:31270`, auth header `DTGCommKey: <CommAPIKey.txt>`.
Subscription form: `/subscription/<path>?Subscription=1`.

## AFB set speed  ✅
- `CurrentDrivableActor.Function.IS_GetTargetSpeedMS` → `{ "Speed": <m/s>, "IsActive": <bool> }`
  - `IsActive=false` when AFB not engaged; `Speed` is the AFB target in m/s (× 3.6 → km/h).
- Alt: `CurrentDrivableActor.Property.Simulation_TargetSpeedMS_InputValue` (node ref, not a value — skip).

## PZB  — node `CurrentDrivableActor/PZB_Service_V3`
Clean scalar/bool properties (`{Value: ...}`):
- `Property.bIsPZB_Active` → PZB system engaged
- `Property.bIsLZB_Active`, `Property.bIsGNT_Active`
- `Property._InEmergency` → PZB Zwangsbremsung (forced emergency brake) active
- `Property._RequiresAcknowledge` → must press Wachsam (acknowledge)
- `Property.ActiveMode` (int; 0 = none) — monitoring mode (U/M/O)
- `Property.influenceCode` (int; -1 = none)
- `Property.MaxSpeed` → `{value: m/s}`

Magnet influence struct — `Property.ActiveInfluence` and `Function.Get_InfluenceState`
(→ `{influenceState: {...}}`). **Struct keys carry FGuid hash suffixes that change between
builds — MATCH BY PREFIX.** Key flags:
- `2000Hz_Active*`  → **passing a signal at danger / Halt** (this is the "signal at danger" PZB trip)
- `500Hz_Active*`   → 500 Hz magnet (approach to a Halt signal)
- `1000Hz_Active*`  → 1000 Hz magnet (distant signal at caution)
- `isRestricted*`, `isOverspeed*`, `isEmergency*`, `isAcknowledged*`
- `speedLimit*` (m/s), `1000Hz_Time*`, `500Hz_Distance*`

## SIFA  — node `CurrentDrivableActor/BP_Sifa_Service`
- `Property.WarningState` → `{Value: bool}` (vigilance warning active)
- `Property.WarningStateVisual`, `Property.WarningStateAuditory` → `{Value: bool}`
- `Property.inPenaltyBrakeApplication` → `{Value: bool}` (Sifa penalty brake)
- `Function.GetWarningState` → `{State: bool}`
- `Function.WarningDevices_GetIsWarningActive` → `{WarningActive: bool}`

## LZB (bonus) — node `CurrentDrivableActor/LZB_Service`
- `Property.bIsActivated`, `Property.bIsEmergencyBraking`, `Property.bIsStopSignalPassed` → `{Value: bool}`
- `Property.maxPermisibleSpeed_ms`, `Property.expectedTargetSpeed_ms`, `Property.monitoringSpeed_ms`,
  `Property.targetSpeedDistance_m`, `Property.currentSpeedLimit_ms` → `{Value: m/s|m}`

## Signal aspect (route-side, already streamed)
- `DriverAid.Data.signalAspectClass` (only `"Clear"` captured on UK earlier; German danger string TBD)
  + `distanceToSignal`. Already parsed in `telemetry.go`.

Generic safety funcs that ERRORed on the BR 442 (don't use):
`GetPenaltyBrakeDemand`, `GetAllSafetySystemsEnabled` → "Node failed to return valid data."
