import React, {useEffect, useRef} from 'react';
import useBaseUrl from '@docusaurus/useBaseUrl';

// ModeVideo renders a looping, muted, inline demo clip. It autoplays everywhere
// muted inline video is permitted, but honors `prefers-reduced-motion` by not
// starting playback for users who asked for less motion — they get the poster
// still plus controls. The poster (a webp first/last frame) also covers the
// case where a browser blocks autoplay (e.g. iOS Low Power Mode).
export default function ModeVideo({src, poster, label}) {
  const ref = useRef(null);

  useEffect(() => {
    const v = ref.current;
    if (!v) return;
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
    if (!reduce.matches) {
      const p = v.play();
      if (p && typeof p.catch === 'function') p.catch(() => {});
    }
  }, []);

  return (
    <video
      ref={ref}
      loop
      muted
      playsInline
      controls
      preload="metadata"
      poster={useBaseUrl(poster)}
      width="100%"
      style={{
        borderRadius: 8,
        border: '1px solid var(--ifm-color-emphasis-200)',
      }}
      aria-label={label}>
      <source src={useBaseUrl(src)} type="video/webm" />
    </video>
  );
}
