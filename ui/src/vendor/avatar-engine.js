// Reused from bauhaus-avatar-lab, generator version 6. Geometry is unchanged.
  const families = ['polygon', 'circle', 'structure', 'figure'];
  const motifs = {
    polygon: ['diamond', 'hexagon', 'trapezoid', 'octagon', 'tile', 'lobes', 'waist', 'puffy-diamond', 'rounded-triangle'],
    circle: ['circle', 'ellipse', 'lens', 'oval', 'ovoid', 'cookie-four', 'cookie-seven'],
    structure: ['heptagon', 'kidney', 'oblique', 'bevel', 'facet', 'soft-knot', 'swell', 'puffy'],
    figure: ['drop', 'seedpod', 'saddle', 'bean', 'petal', 'clover', 'flower-five', 'flower-six', 'flower-eight']
  };
  const cutouts = ['circle','half-circle','square','triangle','horizontal-slot','vertical-slot','oval','flower','diamond','capsule','hexagon','clover','horizontal-split','vertical-split'];
  const edgeShapes = ['diamond','hexagon','trapezoid','octagon','tile','heptagon','oblique','bevel','facet','rounded-triangle'];
  const edges = ['straight','concave-1','convex-1'];
  const edgeOptions = motif => motif==='tile' ? edges.slice(1) : edges;
  function random(seed) {
    let h = 2166136261;
    for (const char of seed) { h ^= char.codePointAt(0); h = Math.imul(h, 16777619); }
    return () => { h += 0x6D2B79F5; let t = h; t = Math.imul(t ^ t >>> 15, t | 1); t ^= t + Math.imul(t ^ t >>> 7, t | 61); return ((t ^ t >>> 14) >>> 0) / 4294967296; };
  }
  // Theme colours never enter the seed: recolouring preserves the geometry.
  export function generate(name, options = {}) {
    const seed = String(name).normalize('NFC').trim() || 'Death Star';
    const variant = Math.max(0, Math.floor(Number(options.variant) || 0));
    const rng = random('floatlab-v1|' + seed + '|' + variant);
    const pick = list => list[Math.floor(rng() * list.length)];
    const seededFamily = families.includes(options.family) ? options.family : pick(families);
    const seededMotif = pick(motifs[seededFamily]);
    const manualFamily = families.find(family=>motifs[family].includes(options.motif));
    const family = manualFamily || seededFamily;
    const motif = manualFamily ? options.motif : seededMotif;
    const requestedEdge = motif==='tile'&&options.edges==='straight'?'concave-1':String(options.edges).replace(/-2$/, '-1');
    const availableEdges = edgeOptions(motif);
    const edge = edgeShapes.includes(motif) ? availableEdges.includes(requestedEdge) ? requestedEdge : availableEdges[Math.floor(random('floatlab-edges-v1|'+seed+'|'+variant)()*availableEdges.length)] : 'straight';
    const detail = ['quiet', 'balanced', 'bold'].includes(options.detail) ? options.detail : 'balanced';
    const ink = /^#[0-9a-f]{6}$/i.test(options.ink) ? options.ink : '#172b2b';
    const paper = /^#[0-9a-f]{6}$/i.test(options.paper) ? options.paper : '#dfff80';
    const hole = cutouts.includes(options.cutout) ? options.cutout : pick(cutouts);
    const turn = pick([0, 90, 180, 270]);
    const size = detail==='bold'?18:14;
    let body = '';
    const circle = (x, y, r, fill = ink) => `<circle cx="${x}" cy="${y}" r="${r}" fill="${fill}"/>`;
    const rect = (x, y, w, h, fill = ink) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="4" fill="${fill}"/>`;
    const path = (d, fill = ink) => `<path d="${d}" fill="${fill}" stroke="${fill}" stroke-width="6" stroke-linejoin="round"/>`;
    // Round both convex and concave corners without adding fine outlines.
    const polygon = (points, fill = ink, rounding = 4) => {
      const vertices = points.split(' ').map(point => point.split(',').map(Number));
      const corners = vertices.map((point, i) => {
        const previous = vertices[(i+vertices.length-1)%vertices.length];
        const next = vertices[(i+1)%vertices.length];
        const toward = target => { const distance = Math.hypot(target[0]-point[0],target[1]-point[1]); const ratio = Math.min(rounding,distance/3)/distance; return [point[0]+(target[0]-point[0])*ratio,point[1]+(target[1]-point[1])*ratio].map(value=>Number(value.toFixed(3))).join(' '); };
        return {entry:toward(previous),exit:toward(next),point:point.join(' ')};
      });
      if(fill===ink&&edge!=='straight'){
        const amount=(edge.startsWith('concave')?-1:1)*.08;
        const area=vertices.reduce((sum,p,i)=>{const q=vertices[(i+1)%vertices.length];return sum+p[0]*q[1]-q[0]*p[1];},0);
        const direction=Math.sign(area);
        const pair=p=>p.map(v=>Number(v.toFixed(3))).join(' ');
        // Trim each curved edge, then join corners along the edge tangents.
        const segments=vertices.map((p,i)=>{
          const q=vertices[(i+1)%vertices.length],c=[(p[0]+q[0])/2+(q[1]-p[1])*amount*direction,(p[1]+q[1])/2-(q[0]-p[0])*amount*direction];
          const t=Math.min(rounding/Math.hypot(q[0]-p[0],q[1]-p[1]),1/3);
          const at=t=>p.map((v,j)=>(1-t)*(1-t)*v+2*(1-t)*t*c[j]+t*t*q[j]);
          const start=at(t),end=at(1-t),control=start.map((v,j)=>v+((1-t)*(c[j]-p[j])+t*(q[j]-c[j]))*(1-2*t));
          return {start,end,control};
        });
        const curves=segments.map((s,i)=>{
          const next=segments[(i+1)%segments.length],handle=Math.hypot(next.start[0]-s.end[0],next.start[1]-s.end[1])*.45;
          const tangent=(point,control)=>{const length=Math.hypot(point[0]-control[0],point[1]-control[1]);return point.map((v,j)=>v+(v-control[j])*handle/length);};
          return `Q${pair(s.control)} ${pair(s.end)} C${pair(tangent(s.end,s.control))} ${pair(tangent(next.start,next.control))} ${pair(next.start)}`;
        });
        return `<path d="M${pair(segments[0].start)} ${curves.join(' ')} Z" fill="${fill}"/>`;
      }
      return `<path d="M${corners[0].entry} ${corners.map(corner=>`L${corner.entry} Q${corner.point} ${corner.exit}`).join(' ')} Z" fill="${fill}"/>`;
    };
    const flower = (x,y,r,petals,amplitude,fill=ink) => {
      const points=Array.from({length:48},(_,i)=>{const angle=i*Math.PI/24-Math.PI/2;const radius=r*(1+amplitude*Math.cos(petals*(angle+Math.PI/2)));return [x+Math.cos(angle)*radius,y+Math.sin(angle)*radius];});
      const at=i=>points[(i+48)%48]; const pair=p=>p.map(v=>Number(v.toFixed(3))).join(' ');
      let d='M'+pair(points[0]);
      for(let i=0;i<48;i++){const a=at(i-1),b=at(i),c=at(i+1),e=at(i+2);d+=' C'+pair([b[0]+(c[0]-a[0])/6,b[1]+(c[1]-a[1])/6])+' '+pair([c[0]-(e[0]-b[0])/6,c[1]-(e[1]-b[1])/6])+' '+pair(c);}
      return `<path d="${d} Z" fill="${fill}"/>`;
    };
    const cut = (x = 64, y = 64, r = size) => {
      const gap = detail==='bold'?18:14;
      if (hole === 'horizontal-split') return rect(0,64-gap/2,128,gap,paper);
      if (hole === 'vertical-split') return rect(64-gap/2,0,gap,128,paper);
      if (hole === 'square') return rect(x-r, y-r, r*2, r*2, paper);
      if (hole === 'diamond') return polygon(`${x},${y-r-3} ${x+r+3},${y} ${x},${y+r+3} ${x-r-3},${y}`, paper);
      if (hole === 'capsule') return rect(x-r-3,y-r+3,r*2+6,r*2-6,paper);
      if (hole === 'triangle') return polygon(`${x},${y-r} ${x+r},${y+r} ${x-r},${y+r}`,paper,7);
      if (hole === 'half-circle') return path(`M${x-r} ${y-r/2} H${x+r} A${r} ${r} 0 0 1 ${x-r} ${y-r/2}Z`,paper);
      if (hole === 'horizontal-slot') return rect(x-r-3,y-7,r*2+6,14,paper);
      if (hole === 'vertical-slot') return rect(x-7,y-r-3,14,r*2+6,paper);
      if (hole === 'oval') return `<ellipse cx="${x}" cy="${y}" rx="${r+3}" ry="${r-4}" fill="${paper}"/>`;
      if (hole === 'flower') return flower(x,y,r*.85,5,.2,paper);
      if (hole === 'hexagon') return polygon(`${x-r},${y} ${x-r/2},${y-r} ${x+r/2},${y-r} ${x+r},${y} ${x+r/2},${y+r} ${x-r/2},${y+r}`,paper,3);
      if (hole === 'clover') return circle(x-r/3,y-r/3,r*.6,paper)+circle(x+r/3,y-r/3,r*.6,paper)+circle(x-r/3,y+r/3,r*.6,paper)+circle(x+r/3,y+r/3,r*.6,paper);
      return circle(x, y, r, paper);
    };
    switch (motif) {
      case 'diamond': body = polygon('64,22 106,64 64,106 22,64'); break;
      case 'hexagon': body = polygon('43,28 85,28 106,64 85,100 43,100 22,64'); break;
      case 'trapezoid': body = polygon('38,28 90,28 104,100 24,100'); break;
      case 'octagon': body = polygon('46,24 82,24 104,46 104,82 82,104 46,104 24,82 24,46'); break;
      case 'tile': body = polygon('26,26 102,26 102,102 26,102'); break;
      case 'lobes': body = circle(47,49,28)+circle(81,59,27)+circle(59,84,25); break;
      case 'waist': body = path('M32 28 H96 Q104 28 100 40 Q82 64 100 88 Q104 100 96 100 H32 Q24 100 28 88 Q46 64 28 40 Q24 28 32 28Z'); break;
      case 'circle': body = circle(64,64,40); break;
      case 'ellipse': body = `<ellipse cx="64" cy="64" rx="42" ry="34" fill="${ink}"/>`; break;
      case 'lens': body = path('M20 64 Q64 6 108 64 Q64 122 20 64Z'); break;
      case 'oval': body = `<ellipse cx="64" cy="64" rx="32" ry="42" fill="${ink}"/>`; break;
      case 'ovoid': body = path('M39 31 C69 14 102 41 99 74 C96 108 51 114 31 87 C16 66 19 43 39 31Z'); break;
      case 'heptagon': body = polygon('54,23 87,31 105,58 97,88 65,106 32,92 22,55'); break;
      case 'kidney': body = path('M33 27 C65 15 107 42 102 80 C97 111 63 108 53 88 C43 71 21 87 23 62 C24 47 19 34 33 27Z'); break;
      case 'oblique': body = polygon('46,26 102,35 82,102 26,91'); break;
      case 'bevel': body = polygon('28,30 76,24 102,49 96,98 46,106 24,79'); break;
      case 'facet': body = polygon('35,25 89,29 107,71 80,104 32,93 23,53'); break;
      case 'soft-knot': body = path('M31 33 C51 12 72 35 85 28 C111 17 113 58 95 74 C108 99 68 116 52 98 C23 111 14 68 34 56 C22 48 23 41 31 33Z'); break;
      case 'swell': body = path('M28 44 C42 13 85 22 99 47 C116 75 94 108 67 102 C45 116 17 93 25 69 C19 56 20 51 28 44Z'); break;
      case 'drop': body = path('M64 24 C73 24 103 60 103 77 C103 115 25 115 25 77 C25 60 55 24 64 24Z'); break;
      case 'seedpod': body = path('M37 28 C72 16 105 42 99 80 C94 113 54 110 32 88 C15 70 18 44 37 28Z'); break;
      case 'saddle': body = path('M31 29 C45 20 51 44 64 44 C77 44 84 20 98 29 C110 46 104 87 87 100 C74 110 52 110 39 100 C22 87 19 46 31 29Z'); break;
      case 'bean': body = path('M29 43 C40 17 87 20 102 48 C110 67 89 68 83 82 C78 110 34 111 25 82 C18 67 20 55 29 43Z'); break;
      case 'petal': body = path('M28 31 C56 16 91 30 103 58 C110 82 85 112 60 101 C34 94 18 57 28 31Z'); break;
      case 'rounded-triangle': body = polygon('64,24 106,100 22,100',ink,14); break;
      case 'clover': body = circle(45,45,25)+circle(83,45,25)+circle(45,83,25)+circle(83,83,25); break;
      case 'flower-five': body = flower(64,64,35,5,.22); break;
      case 'flower-six': body = flower(64,64,36,6,.18); break;
      case 'flower-eight': body = flower(64,64,37,8,.14); break;
      case 'cookie-four': body = flower(64,64,38,4,.12); break;
      case 'cookie-seven': body = flower(64,64,38,7,.1); break;
      case 'puffy': body = flower(64,64,37,4,.2); break;
      case 'puffy-diamond': body = path('M64 24 C83 24 83 42 104 64 C104 83 86 83 64 104 C45 104 45 86 24 64 C24 45 42 45 64 24Z'); break;
    }
    const rotation=family==='figure'||motif==='rounded-triangle'?0:[0,90,180,270].includes(options.rotation)?options.rotation:turn;
    const cutY=motif==='rounded-triangle'?80:motif==='saddle'?72:64;
    const cutSize=['rounded-triangle','saddle','waist'].includes(motif)?12:size;
    const split=detail!=='quiet'&&hole.endsWith('-split');
    const id='cut-'+Math.floor(random([motif,edge,hole,rotation,detail].join('|'))()*4294967296).toString(36);
    const negative=cut(64,cutY,cutSize).replaceAll(paper,'#000000');
    const mask=detail==='quiet'?'':`<mask id="${id}" maskUnits="userSpaceOnUse" x="0" y="0" width="128" height="128"><rect width="128" height="128" rx="0" fill="#ffffff"/>${negative}</mask>`;
    // Round the alpha silhouette after splitting, including the new intersections.
    const rounding=split?`<filter id="split-round" filterUnits="userSpaceOnUse" x="0" y="0" width="128" height="128"><feGaussianBlur in="SourceAlpha" stdDeviation="2.4"/><feColorMatrix type="matrix" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 18 -8.5" result="rounded"/><feComposite in="SourceGraphic" in2="rounded" operator="in"/></filter>`:'';
    const shape=`<g${detail==='quiet'?'':` mask="url(#${id})"`}><g transform="rotate(${rotation} 64 64)">${body}</g></g>`;
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128"><defs>${mask}${rounding}</defs>${split?`<g filter="url(#split-round)">${shape}</g>`:shape}</svg>`;
    return {svg, seed, variant, family, motif, edge, hole, turn:rotation, scale:1, version:'6'};
  }
