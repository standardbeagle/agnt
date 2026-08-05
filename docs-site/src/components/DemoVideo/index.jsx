import React from 'react';
import useBaseUrl from '@docusaurus/useBaseUrl';

// DemoVideo renders a narrated, sound-on demo. Unlike ModeVideo (muted,
// looping, autoplaying feature clips), these are click-to-play with audio,
// so no autoplay and no loop. Captions are burned into the demo renders;
// an optional VTT track is attached anyway (default off) so text is
// available to assistive tech and search.
export default function DemoVideo({src, poster, captions, label}) {
  const captionsUrl = useBaseUrl(captions || '');
  return (
    <video
      controls
      playsInline
      preload="metadata"
      poster={useBaseUrl(poster)}
      width="100%"
      style={{
        borderRadius: 8,
        border: '1px solid var(--ifm-color-emphasis-200)',
      }}
      aria-label={label}>
      <source src={useBaseUrl(src)} type="video/webm" />
      {captions ? (
        <track kind="captions" src={captionsUrl} srcLang="en" label="English" />
      ) : null}
    </video>
  );
}
