import { z as e } from "zod";
import "svelte/internal/disclose-version";
import "svelte/internal/flags/legacy";
import * as t from "svelte/internal/client";
import { GaugeChart as n, LineChart as r } from "echarts/charts";
import { AriaComponent as i, GraphicComponent as a, GridComponent as o, LegendComponent as s, MarkLineComponent as c, TooltipComponent as l } from "echarts/components";
import { init as u, use as d } from "echarts/core";
import { CanvasRenderer as f } from "echarts/renderers";
import { onMount as p } from "svelte";
//#region src/lib/constants.ts
var m = 30318, h = "dashboard-widget/v1", g = 1, _ = 32768, v = 2e3, y = ["wss://relay.sharegap.net", "wss://nos.lol"], b = [
	"timeseries",
	"gauge",
	"stat",
	"event_table"
], x = [
	"ops.timeseries.v1",
	"ops.gauge.v1",
	"ops.stat.v1",
	"ops.event_table.v1"
], S = {
	timeseries: "ops.timeseries.v1",
	gauge: "ops.gauge.v1",
	stat: "ops.stat.v1",
	event_table: "ops.event_table.v1"
}, C = e.union([
	e.string(),
	e.number().finite(),
	e.boolean(),
	e.null()
]), w = {
	"ops.timeseries.v1": e.object({
		timestamps: e.array(e.number().int().nonnegative()).min(1),
		series: e.array(e.object({
			name: e.string().min(1).max(160),
			values: e.array(e.union([e.number().finite(), e.null()])).min(1)
		}).passthrough()).min(1)
	}).passthrough().superRefine((e, t) => {
		for (let [n, r] of e.series.entries()) r.values.length !== e.timestamps.length && t.addIssue({
			code: "custom",
			path: [
				"series",
				n,
				"values"
			],
			message: "series values must match timestamps length"
		});
		e.series.reduce((e, t) => e + t.values.length, 0) > 2e3 && t.addIssue({
			code: "custom",
			path: ["series"],
			message: `timeseries exceeds ${v} total points`
		});
	}),
	"ops.gauge.v1": e.object({
		value: e.number().finite(),
		min: e.number().finite(),
		max: e.number().finite()
	}).passthrough().superRefine((e, t) => {
		e.min >= e.max && t.addIssue({
			code: "custom",
			path: ["min"],
			message: "gauge min must be less than max"
		}), (e.value < e.min || e.value > e.max) && t.addIssue({
			code: "custom",
			path: ["value"],
			message: "gauge value must be within min and max"
		});
	}),
	"ops.stat.v1": e.object({
		value: e.union([e.number().finite(), e.string().min(1).max(160)]),
		label: e.string().max(160).optional(),
		change: e.number().finite().optional()
	}).passthrough(),
	"ops.event_table.v1": e.object({
		columns: e.array(e.string().min(1).max(120)).min(1).max(64),
		rows: e.array(e.array(C)).max(v)
	}).passthrough().superRefine((e, t) => {
		for (let [n, r] of e.rows.entries()) r.length !== e.columns.length && t.addIssue({
			code: "custom",
			path: ["rows", n],
			message: "row width must match columns length"
		});
	})
};
function T(e, t) {
	let n = w[e].safeParse(t);
	if (n.success) return {
		ok: !0,
		data: n.data
	};
	let r = n.error.issues.map((e) => `${e.path.length > 0 ? `${e.path.join(".")}: ` : ""}${e.message}`);
	return {
		ok: !1,
		issues: r,
		capExceeded: r.some((e) => e.includes(`exceeds ${v}`))
	};
}
//#endregion
//#region src/lib/store.ts
function E(e, t) {
	return e.created_at > t.created_at || e.created_at === t.created_at && e.id.localeCompare(t.id) > 0;
}
function D(e) {
	let t = new Set(e.allowedPubkeys.map((e) => e.toLowerCase())), n = /* @__PURE__ */ new Set(), r = /* @__PURE__ */ new Map(), i = /* @__PURE__ */ new Set(), a = () => [...r.values()].sort((e, t) => t.created_at - e.created_at || t.id.localeCompare(e.id)), o = () => {
		let e = a();
		for (let t of i) t(e);
	};
	return {
		subscribe(e) {
			return i.add(e), e(a()), () => i.delete(e);
		},
		ingest(e) {
			if (!t.has(e.pubkey.toLowerCase())) return {
				accepted: !1,
				reason: "untrusted_publisher"
			};
			if (e.kind !== 30318) return {
				accepted: !1,
				reason: "wrong_kind"
			};
			if (n.has(e.id)) return {
				accepted: !1,
				reason: "duplicate"
			};
			n.add(e.id);
			let i = e.tags.find((e) => e[0] === "d")?.[1];
			if (!i) return {
				accepted: !1,
				reason: "missing_d_tag"
			};
			let a = `${e.pubkey.toLowerCase()}\u0000${i}`, s = r.get(a);
			return s && !E(e, s) ? {
				accepted: !1,
				reason: "stale"
			} : (r.set(a, e), o(), {
				accepted: !0,
				action: s ? "replaced" : "inserted"
			});
		},
		clear() {
			n.clear(), r.clear(), o();
		}
	};
}
//#endregion
//#region src/lib/validator.ts
var O = e.string().min(1).max(200).refine((e) => !e.includes(":"), { message: "slot key parts cannot contain colons" }), ee = e.object({
	title: e.string().trim().min(1).max(240),
	alt: e.string().trim().min(1).max(1e3),
	subtitle: e.string().max(500).optional(),
	unit: e.string().max(80).optional()
}).passthrough(), te = e.object({
	metric: O,
	host: O,
	service: O
}).passthrough(), ne = e.object({
	from: e.number().int().nonnegative(),
	to: e.number().int().nonnegative(),
	step: e.number().int().positive(),
	generated_at: e.number().int().nonnegative(),
	staleness_ttl: e.number().int().nonnegative(),
	window: O
}).passthrough().refine((e) => e.from <= e.to, {
	path: ["to"],
	message: "query.to must be greater than or equal to query.from"
}), re = e.object({
	op: e.enum([
		">",
		">=",
		"<",
		"<=",
		"=="
	]),
	value: e.number().finite(),
	severity: e.enum([
		"info",
		"warn",
		"critical"
	]),
	label: e.string().max(160).optional()
}).passthrough(), k = e.object({
	url: e.url().max(2048),
	sha256: e.string().regex(/^[0-9a-fA-F]{64}$/, "sha256 must be 64 hexadecimal characters"),
	media_type: e.literal("application/json").optional()
}).passthrough(), A = e.object({
	type: e.literal("dashboard_widget"),
	version: e.literal(1),
	widget_kind: e.enum(b),
	meta: ee,
	scope: te,
	query: ne,
	data: e.unknown().optional(),
	data_ref: k.optional(),
	presentation: e.object({
		template: e.enum(x),
		thresholds: e.array(re).max(32).optional().default([])
	}).passthrough()
}).passthrough().superRefine((e, t) => {
	e.data !== void 0 == (e.data_ref !== void 0) && t.addIssue({
		code: "custom",
		message: "exactly one of data or data_ref is required"
	}), "renderer" in e && t.addIssue({
		code: "custom",
		path: ["renderer"],
		message: "renderer is forbidden in dashboard-widget/v1"
	}), "spec" in e && t.addIssue({
		code: "custom",
		path: ["spec"],
		message: "spec is forbidden in dashboard-widget/v1"
	}), "window" in e.scope && t.addIssue({
		code: "custom",
		path: ["scope", "window"],
		message: "window belongs in query, not scope"
	});
});
function j(e) {
	return typeof e == "object" && !!e && !Array.isArray(e);
}
function M(e) {
	let t = {};
	for (let n of e) n.length < 2 || !n[0] || (t[n[0]] ??= []).push(n[1]);
	return t;
}
function N(e, t) {
	let n = Array.isArray(e.tags) ? M(e.tags) : {}, r = j(t) && j(t.meta) ? t.meta : void 0;
	return {
		title: typeof r?.title == "string" && r.title.trim() ? r.title : "Dashboard widget",
		alt: typeof r?.alt == "string" && r.alt.trim() ? r.alt : "Widget data could not be rendered safely.",
		tags: n
	};
}
function P(e, t, n, r, i) {
	return {
		ok: !1,
		rejection: {
			code: t,
			message: n,
			issues: i,
			fallback: N(e, r)
		}
	};
}
function F(e) {
	return j(e) ? typeof e.id == "string" && typeof e.pubkey == "string" && Number.isInteger(e.created_at) && Number.isInteger(e.kind) && typeof e.content == "string" && Array.isArray(e.tags) && e.tags.every((e) => Array.isArray(e) && e.every((e) => typeof e == "string")) : !1;
}
function I(e) {
	if (!F(e)) return P(j(e) ? e : {}, "invalid_event", "Invalid Nostr event shape");
	if (e.kind !== 30318) return P(e, "wrong_kind", `Expected kind ${m}`);
	let t = e.tags.filter((e) => e[0] === "d" && typeof e[1] == "string");
	if (t.length === 0 || !t[0][1]) return P(e, "missing_d_tag", "Addressable widget event requires a non-empty d tag");
	if (t.length !== 1) return P(e, "invalid_d_tag", "Addressable widget event requires exactly one d tag");
	let n = t[0][1];
	if (!/^[^:]+:[^:]+:[^:]+:[^:]+$/.test(n)) return P(e, "invalid_d_tag", "d tag must be <metric>:<host>:<service>:<window>");
	if (new TextEncoder().encode(e.content).byteLength > 32768) return P(e, "oversized_content", `Content exceeds ${_} bytes`);
	let r;
	try {
		r = JSON.parse(e.content);
	} catch {
		return P(e, "invalid_json", "Widget content is not valid JSON");
	}
	if (!j(r)) return P(e, "invalid_envelope", "Widget content must be a JSON object", r);
	if (r.version !== 1) return P(e, "unsupported_version", `Unsupported dashboard widget version: ${String(r.version)}`, r);
	if (typeof r.widget_kind != "string" || !b.includes(r.widget_kind)) return P(e, "unsupported_widget_kind", `Unsupported widget_kind: ${String(r.widget_kind)}`, r);
	if (!j(r.presentation) || typeof r.presentation.template != "string" || !x.includes(r.presentation.template)) return P(e, "unsupported_template", "Unsupported or missing presentation.template", r);
	let i = A.safeParse(r);
	if (!i.success) {
		let t = i.error.issues.map((e) => `${e.path.length > 0 ? `${e.path.join(".")}: ` : ""}${e.message}`);
		return P(e, "invalid_envelope", "Widget envelope failed validation", r, t);
	}
	let a = i.data, o = S[a.widget_kind];
	if (a.presentation.template !== o) return P(e, "template_mismatch", `${a.widget_kind} requires template ${o}`, a);
	let s = [
		a.scope.metric,
		a.scope.host,
		a.scope.service,
		a.query.window
	].join(":");
	if (n !== s) return P(e, "invalid_d_tag", `d tag must match canonical scope slot ${s}`, a);
	let c = {
		eventId: e.id,
		pubkey: e.pubkey,
		createdAt: e.created_at,
		dTag: n,
		tags: M(e.tags),
		envelope: a
	};
	if (a.data_ref !== void 0) return {
		ok: !0,
		model: {
			...c,
			source: "data_ref",
			dataRef: a.data_ref
		}
	};
	let l = T(a.presentation.template, a.data);
	return l.ok ? (a.data = l.data, {
		ok: !0,
		model: {
			...c,
			source: "inline",
			data: l.data
		}
	}) : P(e, l.capExceeded ? "cap_exceeded" : "invalid_data", "Template data failed validation", a, l.issues);
}
//#endregion
//#region src/lib/templates/theme.ts
var L = {
	text: "#e8edf2",
	muted: "#768390",
	info: "#4493f8",
	warn: "#d29922",
	critical: "#f85149"
};
function R(e) {
	return {
		...L,
		...e
	};
}
function z(e, t) {
	return t[e];
}
function ie(e, t) {
	switch (t.op) {
		case ">": return e > t.value;
		case ">=": return e >= t.value;
		case "<": return e < t.value;
		case "<=": return e <= t.value;
		case "==": return e === t.value;
	}
}
function B(e, t) {
	let n = {
		info: 1,
		warn: 2,
		critical: 3
	};
	return t.filter((t) => ie(e, t)).map((e) => e.severity).sort((e, t) => n[t] - n[e])[0];
}
//#endregion
//#region src/lib/templates/builders.ts
function V(e, t) {
	return {
		silent: !0,
		symbol: "none",
		data: e.map((e) => ({
			name: e.label ?? `${e.op} ${e.value}`,
			yAxis: e.value,
			label: { formatter: e.label ?? `${e.severity}: ${e.value}` },
			lineStyle: {
				color: z(e.severity, t),
				type: "dashed",
				width: 2
			}
		}))
	};
}
function H(e, t) {
	let n = e.data, r = R(t), i = e.envelope.presentation.thresholds;
	return {
		animation: !1,
		aria: {
			enabled: !0,
			description: e.envelope.meta.alt
		},
		tooltip: { trigger: "axis" },
		legend: { textStyle: { color: r.text } },
		grid: {
			left: 48,
			right: 24,
			top: 40,
			bottom: 40,
			containLabel: !0
		},
		xAxis: {
			type: "time",
			axisLabel: { color: r.muted }
		},
		yAxis: {
			type: "value",
			name: e.envelope.meta.unit,
			nameTextStyle: { color: r.muted },
			axisLabel: { color: r.muted },
			splitLine: { lineStyle: {
				color: r.muted,
				opacity: .2
			} }
		},
		series: n.series.map((e, t) => ({
			name: e.name,
			type: "line",
			showSymbol: !1,
			connectNulls: !1,
			data: n.timestamps.map((t, n) => [t * 1e3, e.values[n]]),
			markLine: t === 0 && i.length > 0 ? V(i, r) : void 0
		}))
	};
}
function U(e, t) {
	let n = e.data, r = R(t), i = B(n.value, e.envelope.presentation.thresholds);
	return {
		animation: !1,
		aria: {
			enabled: !0,
			description: e.envelope.meta.alt
		},
		series: [{
			type: "gauge",
			min: n.min,
			max: n.max,
			progress: {
				show: !0,
				itemStyle: { color: i ? z(i, r) : r.info }
			},
			axisLine: { lineStyle: {
				color: [[1, r.muted]],
				opacity: .35
			} },
			axisLabel: { color: r.muted },
			title: { color: r.muted },
			detail: {
				color: i ? z(i, r) : r.text,
				formatter: e.envelope.meta.unit ? `{value} ${e.envelope.meta.unit}` : "{value}"
			},
			data: [{
				value: n.value,
				name: e.envelope.meta.title
			}]
		}]
	};
}
function W(e, t) {
	let n = e.data, r = R(t), i = typeof n.value == "number" ? n.value : void 0, a = i === void 0 ? void 0 : B(i, e.envelope.presentation.thresholds), o = `${n.value}${e.envelope.meta.unit ? ` ${e.envelope.meta.unit}` : ""}`, s = n.change === void 0 ? "" : `${n.change >= 0 ? "+" : ""}${n.change}`;
	return {
		animation: !1,
		aria: {
			enabled: !0,
			description: e.envelope.meta.alt
		},
		graphic: [
			{
				type: "text",
				left: "center",
				top: "32%",
				style: {
					text: o,
					fill: a ? z(a, r) : r.text,
					fontSize: 34,
					fontWeight: 700
				}
			},
			{
				type: "text",
				left: "center",
				top: "58%",
				style: {
					text: n.label ?? e.envelope.meta.title,
					fill: r.muted,
					fontSize: 14
				}
			},
			...s ? [{
				type: "text",
				left: "center",
				top: "72%",
				style: {
					text: s,
					fill: r.muted,
					fontSize: 13
				}
			}] : []
		]
	};
}
//#endregion
//#region src/lib/templates/registry.ts
var G = {
	"ops.timeseries.v1": {
		name: "ops.timeseries.v1",
		widgetKind: "timeseries",
		renderTarget: "echarts",
		buildOption: H
	},
	"ops.gauge.v1": {
		name: "ops.gauge.v1",
		widgetKind: "gauge",
		renderTarget: "echarts",
		buildOption: U
	},
	"ops.stat.v1": {
		name: "ops.stat.v1",
		widgetKind: "stat",
		renderTarget: "echarts",
		buildOption: W
	},
	"ops.event_table.v1": {
		name: "ops.event_table.v1",
		widgetKind: "event_table",
		renderTarget: "table"
	}
};
function K(e) {
	return G[e];
}
function q(e, t) {
	let n = G[e.envelope.presentation.template];
	if (n.renderTarget !== "echarts" || !n.buildOption) throw Error(`Template ${n.name} does not render with ECharts`);
	return n.buildOption(e, t);
}
//#endregion
//#region src/lib/components/FallbackCard.svelte
var J = t.from_html("<div class=\"svelte-1r3e8sc\"><dt class=\"svelte-1r3e8sc\"> </dt><dd class=\"svelte-1r3e8sc\"> </dd></div>"), ae = t.from_html("<dl class=\"svelte-1r3e8sc\"></dl>"), oe = t.from_html("<article class=\"fallback svelte-1r3e8sc\"><div class=\"eyebrow svelte-1r3e8sc\">Widget fallback</div> <h3 class=\"svelte-1r3e8sc\"> </h3> <p class=\"svelte-1r3e8sc\"> </p> <p class=\"reason svelte-1r3e8sc\"> </p> <!></article>");
function Y(e, n) {
	t.push(n, !1);
	let r = t.prop(n, "metadata", 8), i = t.prop(n, "reason", 8, "Unsupported widget payload");
	t.init();
	var a = oe(), o = t.sibling(t.child(a), 2), s = t.only_child(o, !0), c = t.sibling(o, 2), l = t.only_child(c, !0), u = t.sibling(c, 2), d = t.only_child(u, !0), f = t.sibling(u, 2), p = (e) => {
		var n = ae();
		t.each(n, 5, () => (t.deep_read_state(r()), t.untrack(() => Object.entries(r().tags))), t.index, (e, n) => {
			var r = t.derived(() => t.to_array(t.get(n), 2));
			let i = () => t.get(r)[0], a = () => t.get(r)[1];
			var o = J(), s = t.child(o), c = t.only_child(s, !0), l = t.sibling(s), u = t.only_child(l, !0);
			t.reset(o), t.template_effect((e) => {
				t.set_text(c, i()), t.set_text(u, e);
			}, [() => (a(), t.untrack(() => a().join(", ")))]), t.append(e, o);
		}), t.reset(n), t.append(e, n);
	}, m = t.derived(() => (t.deep_read_state(r()), t.untrack(() => Object.keys(r().tags).length > 0)));
	t.if(f, (e) => {
		t.get(m) && e(p);
	}), t.reset(a), t.template_effect(() => {
		t.set_attribute(a, "aria-label", (t.deep_read_state(r()), t.untrack(() => r().alt))), t.set_text(s, (t.deep_read_state(r()), t.untrack(() => r().title))), t.set_text(l, (t.deep_read_state(r()), t.untrack(() => r().alt))), t.set_text(d, i());
	}), t.append(e, a), t.pop();
}
//#endregion
//#region src/lib/components/DataRefPlaceholder.svelte
var se = t.from_html("<article class=\"placeholder svelte-1y24a2a\"><div class=\"eyebrow svelte-1y24a2a\">External widget data</div> <h3 class=\"svelte-1y24a2a\"> </h3> <p class=\"svelte-1y24a2a\"> </p> <strong class=\"svelte-1y24a2a\">sidecar fetch not implemented</strong> <code class=\"svelte-1y24a2a\"> </code></article>");
function X(e, n) {
	t.push(n, !1);
	let r = t.prop(n, "meta", 8), i = t.prop(n, "dataRef", 8);
	t.init();
	var a = se(), o = t.sibling(t.child(a), 2), s = t.only_child(o, !0), c = t.sibling(o, 2), l = t.only_child(c, !0), u = t.sibling(c, 4), d = t.only_child(u);
	t.reset(a), t.template_effect((e) => {
		t.set_attribute(a, "aria-label", (t.deep_read_state(r()), t.untrack(() => r().alt))), t.set_text(s, (t.deep_read_state(r()), t.untrack(() => r().title))), t.set_text(l, (t.deep_read_state(r()), t.untrack(() => r().alt))), t.set_attribute(u, "title", (t.deep_read_state(i()), t.untrack(() => i().url))), t.set_text(d, `sha256:${e ?? ""}…`);
	}, [() => (t.deep_read_state(i()), t.untrack(() => i().sha256.slice(0, 12)))]), t.append(e, a), t.pop();
}
//#endregion
//#region src/lib/components/EChartsWidget.svelte
var ce = t.from_html("<div class=\"chart svelte-11fpn3y\" role=\"img\" aria-label=\"Widget chart\"></div>");
function le(e, m) {
	t.push(m, !1), d([
		r,
		n,
		i,
		a,
		o,
		s,
		c,
		l,
		f
	]);
	let h = t.prop(m, "option", 8), g = t.prop(m, "height", 8, "260px"), _ = t.mutable_source(), v = t.mutable_source();
	p(() => {
		t.set(v, u(t.get(_))), t.get(v).setOption(h(), !0);
		let e = new ResizeObserver(() => t.get(v)?.resize());
		return e.observe(t.get(_)), () => {
			e.disconnect(), t.get(v)?.dispose();
		};
	}), t.legacy_pre_effect(() => (t.get(v), t.deep_read_state(h())), () => {
		t.get(v) && t.get(v).setOption(h(), !0);
	}), t.legacy_pre_effect_reset(), t.init();
	var y = ce();
	let b;
	t.bind_this(y, (e) => t.set(_, e), () => t.get(_)), t.template_effect(() => b = t.set_style(y, "", b, { height: g() })), t.append(e, y), t.pop();
}
//#endregion
//#region src/lib/components/EventTable.svelte
var ue = t.from_html("<th scope=\"col\" class=\"svelte-1310os8\"> </th>"), de = t.from_html("<td class=\"svelte-1310os8\"> </td>"), Z = t.from_html("<tr></tr>"), fe = t.from_html("<div class=\"table-wrap svelte-1310os8\" role=\"region\"><table class=\"svelte-1310os8\"><caption class=\"svelte-1310os8\"> </caption><thead><tr></tr></thead><tbody></tbody></table></div>");
function Q(e, n) {
	t.push(n, !1);
	let r = t.prop(n, "data", 8), i = t.prop(n, "meta", 8);
	t.init();
	var a = fe(), o = t.child(a), s = t.child(o), c = t.only_child(s, !0), l = t.sibling(s), u = t.child(l);
	t.each(u, 5, () => (t.deep_read_state(r()), t.untrack(() => r().columns)), t.index, (e, n) => {
		var r = ue(), i = t.only_child(r, !0);
		t.template_effect(() => t.set_text(i, t.get(n))), t.append(e, r);
	}), t.reset(u), t.reset(l);
	var d = t.sibling(l);
	t.each(d, 5, () => (t.deep_read_state(r()), t.untrack(() => r().rows)), t.index, (e, n) => {
		var r = Z();
		t.each(r, 5, () => t.get(n), t.index, (e, n) => {
			var r = de(), i = t.only_child(r, !0);
			t.template_effect(() => t.set_text(i, t.get(n) ?? "—")), t.append(e, r);
		}), t.reset(r), t.append(e, r);
	}), t.reset(d), t.reset(o), t.reset(a), t.template_effect(() => {
		t.set_attribute(a, "aria-label", (t.deep_read_state(i()), t.untrack(() => i().alt))), t.set_text(c, (t.deep_read_state(i()), t.untrack(() => i().title)));
	}), t.append(e, a), t.pop();
}
//#endregion
//#region src/lib/components/WidgetCard.svelte
var pe = t.from_html("<p class=\"svelte-g2dgjm\"> </p>"), me = t.from_html("<header class=\"svelte-g2dgjm\"><div><h3 class=\"svelte-g2dgjm\"> </h3> <!></div> <span class=\"svelte-g2dgjm\"> </span></header> <!>", 1), he = t.from_html("<article class=\"widget-card svelte-g2dgjm\"><!></article>");
function $(e, n) {
	t.push(n, !1);
	let r = t.mutable_source(), i = t.mutable_source(), a = t.prop(n, "model", 8), o = t.prop(n, "theme", 8, void 0);
	t.legacy_pre_effect(() => t.deep_read_state(a()), () => {
		t.set(r, K(a().envelope.presentation.template));
	}), t.legacy_pre_effect(() => (t.deep_read_state(a()), t.get(r), t.deep_read_state(o())), () => {
		t.set(i, a().source === "inline" && t.get(r).renderTarget === "echarts" ? q(a(), o()) : void 0);
	}), t.legacy_pre_effect_reset(), t.init();
	var s = he(), c = t.child(s), l = (e) => {
		X(e, {
			get meta() {
				return t.deep_read_state(a()), t.untrack(() => a().envelope.meta);
			},
			get dataRef() {
				return t.deep_read_state(a()), t.untrack(() => a().dataRef);
			}
		});
	}, u = (e) => {
		Q(e, {
			get data() {
				return t.deep_read_state(a()), t.untrack(() => a().data);
			},
			get meta() {
				return t.deep_read_state(a()), t.untrack(() => a().envelope.meta);
			}
		});
	}, d = (e) => {
		var n = me(), r = t.first_child(n), o = t.child(r), s = t.child(o), c = t.only_child(s, !0), l = t.sibling(s, 2), u = (e) => {
			var n = pe(), r = t.only_child(n, !0);
			t.template_effect(() => t.set_text(r, (t.deep_read_state(a()), t.untrack(() => a().envelope.meta.subtitle)))), t.append(e, n);
		};
		t.if(l, (e) => {
			t.deep_read_state(a()), t.untrack(() => a().envelope.meta.subtitle) && e(u);
		}), t.reset(o);
		var d = t.sibling(o, 2), f = t.only_child(d, !0);
		t.reset(r), le(t.sibling(r, 2), { get option() {
			return t.get(i);
		} }), t.template_effect(() => {
			t.set_text(c, (t.deep_read_state(a()), t.untrack(() => a().envelope.meta.title))), t.set_text(f, (t.deep_read_state(a()), t.untrack(() => a().envelope.query.window)));
		}), t.append(e, n);
	};
	t.if(c, (e) => {
		t.deep_read_state(a()), t.untrack(() => a().source === "data_ref") ? e(l) : (t.get(r), t.untrack(() => t.get(r).renderTarget === "table") ? e(u, 1) : t.get(i) && e(d, 2));
	}), t.reset(s), t.append(e, s), t.pop();
}
//#endregion
//#region src/lib/components/WidgetRenderer.svelte
function ge(e, n) {
	t.push(n, !1);
	let r = t.mutable_source(), i = t.prop(n, "event", 8), a = t.prop(n, "theme", 8, void 0);
	t.legacy_pre_effect(() => t.deep_read_state(i()), () => {
		t.set(r, I(i()));
	}), t.legacy_pre_effect_reset(), t.init();
	var o = t.comment(), s = t.first_child(o), c = (e) => {
		$(e, {
			get model() {
				return t.get(r), t.untrack(() => t.get(r).model);
			},
			get theme() {
				return a();
			}
		});
	}, l = (e) => {
		{
			let n = t.derived_safe_equal(() => (t.get(r), t.untrack(() => `${t.get(r).rejection.code}: ${t.get(r).rejection.message}`)));
			Y(e, {
				get metadata() {
					return t.get(r), t.untrack(() => t.get(r).rejection.fallback);
				},
				get reason() {
					return t.get(n);
				}
			});
		}
	};
	t.if(s, (e) => {
		t.get(r), t.untrack(() => t.get(r).ok) ? e(c) : e(l, -1);
	}), t.append(e, o), t.pop();
}
//#endregion
export { m as DASHBOARD_WIDGET_KIND, h as DASHBOARD_WIDGET_SCHEMA, g as DASHBOARD_WIDGET_VERSION, L as DEFAULT_WIDGET_THEME, X as DataRefPlaceholder, Q as EventTable, y as FLEET_RELAY_URLS, Y as FallbackCard, _ as MAX_CONTENT_BYTES, v as MAX_POINTS_OR_ROWS, S as TEMPLATE_FOR_WIDGET_KIND, x as TEMPLATE_NAMES, b as WIDGET_KINDS, $ as WidgetCard, ge as WidgetRenderer, B as activeSeverity, q as buildEChartsOption, U as buildGaugeOption, W as buildStatOption, H as buildTimeseriesOption, D as createWidgetStore, w as dataSchemas, K as getTemplate, T as parseTemplateData, R as resolveTheme, z as severityColor, M as tagsToRecord, G as templateRegistry, I as validateWidgetEvent };
