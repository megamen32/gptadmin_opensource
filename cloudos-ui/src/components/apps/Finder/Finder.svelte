<script lang="ts">
        // Finder is the real GrepMesh Console embedded as an iframe: hosts,
        // locations, full-text search and the file viewer/editor live there.
        const GREPMESH_URL = 'https://grep.bezrabotnyi.com/ui/';

        let frame: HTMLIFrameElement;

        function reload() {
                if (frame) frame.src = GREPMESH_URL;
        }
</script>

<section class="finder">
        <header class="titlebar app-window-drag-handle">
                <h1>GrepMesh</h1>
                <span class="sub">all devices · full-text search</span>
                <button class="reload" onclick={reload} aria-label="Reload GrepMesh">⟳</button>
        </header>

        <iframe
                bind:this={frame}
                src={GREPMESH_URL}
                title="GrepMesh Console"
                allow="clipboard-write"
        ></iframe>
</section>

<style>
        .finder {
                height: 100%;
                display: grid;
                grid-template-rows: auto 1fr;

                background-color: hsla(0, 0%, 100%, 0.86);
                backdrop-filter: blur(12px);
                border-radius: inherit;
                color: #1d1d1f;
                font-size: 0.8rem;
                overflow: hidden;
        }

        .titlebar {
                display: flex;
                align-items: baseline;
                gap: 0.6rem;
                padding: 0.45rem 0.9rem;
                background-color: hsla(0, 0%, 92%, 0.7);
                border-bottom: 0.2px solid #0002;
        }
        .titlebar h1 {
                margin: 0;
                font-size: 0.9rem;
                font-weight: 600;
        }
        .sub {
                color: #6e6e73;
                font-size: 0.7rem;
                flex: 1;
        }

        .reload {
                background-color: hsla(0, 0%, 92%, 0.9);
                border: 0.2px solid #0002;
                border-radius: 0.35rem;
                color: #1d1d1f;
                width: 1.4rem;
                height: 1.4rem;
                font-size: 0.75rem;
        }

        iframe {
                width: 100%;
                height: 100%;
                border: 0;
                background-color: #16181c;
        }
</style>
