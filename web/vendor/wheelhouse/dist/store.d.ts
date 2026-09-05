import { NostrWidgetEvent } from './types';
export type WidgetIngestResult = {
    accepted: true;
    action: 'inserted' | 'replaced';
} | {
    accepted: false;
    reason: 'untrusted_publisher' | 'wrong_kind' | 'missing_d_tag' | 'duplicate' | 'stale';
};
export interface WidgetStore {
    subscribe(run: (events: readonly NostrWidgetEvent[]) => void): () => void;
    ingest(event: NostrWidgetEvent): WidgetIngestResult;
    clear(): void;
}
export declare function createWidgetStore(options: {
    allowedPubkeys: readonly string[];
}): WidgetStore;
//# sourceMappingURL=store.d.ts.map