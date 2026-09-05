import { z } from 'zod';
import { TemplateName, WidgetData } from './types';
export declare const dataSchemas: {
    readonly 'ops.timeseries.v1': z.ZodObject<{
        timestamps: z.ZodArray<z.ZodNumber>;
        series: z.ZodArray<z.ZodObject<{
            name: z.ZodString;
            values: z.ZodArray<z.ZodUnion<readonly [z.ZodNumber, z.ZodNull]>>;
        }, z.core.$loose>>;
    }, z.core.$loose>;
    readonly 'ops.gauge.v1': z.ZodObject<{
        value: z.ZodNumber;
        min: z.ZodNumber;
        max: z.ZodNumber;
    }, z.core.$loose>;
    readonly 'ops.stat.v1': z.ZodObject<{
        value: z.ZodUnion<readonly [z.ZodNumber, z.ZodString]>;
        label: z.ZodOptional<z.ZodString>;
        change: z.ZodOptional<z.ZodNumber>;
    }, z.core.$loose>;
    readonly 'ops.event_table.v1': z.ZodObject<{
        columns: z.ZodArray<z.ZodString>;
        rows: z.ZodArray<z.ZodArray<z.ZodUnion<readonly [z.ZodString, z.ZodNumber, z.ZodBoolean, z.ZodNull]>>>;
    }, z.core.$loose>;
};
export type DataParseResult = {
    ok: true;
    data: WidgetData;
} | {
    ok: false;
    issues: string[];
    capExceeded: boolean;
};
export declare function parseTemplateData(template: TemplateName, input: unknown): DataParseResult;
//# sourceMappingURL=data-schemas.d.ts.map