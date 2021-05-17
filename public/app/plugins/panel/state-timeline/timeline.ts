import uPlot, { Series, Cursor } from 'uplot';
import { FIXED_UNIT } from '@grafana/ui/src/components/GraphNG/GraphNG';
import { Quadtree, Rect, pointWithin } from 'app/plugins/panel/barchart/quadtree';
import { distribute, SPACE_BETWEEN } from 'app/plugins/panel/barchart/distribute';
import { TimelineFieldConfig } from './types';
import { GrafanaTheme2, TimeRange } from '@grafana/data';
import { BarValueVisibility } from '@grafana/ui';
import tinycolor from 'tinycolor2';

const { round } = Math;

const textPadding = 2;

const pxRatio = devicePixelRatio;

const laneDistr = SPACE_BETWEEN;

type WalkCb = (idx: number, offPx: number, dimPx: number) => void;

function walk(rowHeight: number, yIdx: number | null, count: number, dim: number, draw: WalkCb) {
  distribute(count, rowHeight, laneDistr, yIdx, (i, offPct, dimPct) => {
    let laneOffPx = dim * offPct;
    let laneWidPx = dim * dimPct;

    draw(i, laneOffPx, laneWidPx);
  });
}

interface TimelineBoxRect extends Rect {
  fillColor: string;
}

/**
 * @internal
 */
export interface TimelineCoreOptions {
  numSeries: number;
  rowHeight: number;
  colWidth?: number;
  theme: GrafanaTheme2;
  showValue: BarValueVisibility;
  isDiscrete: (seriesIdx: number) => boolean;
  getValueColor: (seriesIdx: number, value: any) => string;
  label: (seriesIdx: number) => string;
  getTimeRange: () => TimeRange;
  formatValue?: (seriesIdx: number, value: any) => string;
  getFieldConfig: (seriesIdx: number) => TimelineFieldConfig;
  onHover?: (seriesIdx: number, valueIdx: number) => void;
  onLeave?: (seriesIdx: number, valueIdx: number) => void;
}

/**
 * @internal
 */
export function getConfig(opts: TimelineCoreOptions) {
  const {
    numSeries,
    isDiscrete,
    rowHeight = 0,
    showValue,
    theme,
    label,
    formatValue,
    getTimeRange,
    getValueColor,
    getFieldConfig,
    // onHover,
    // onLeave,
  } = opts;

  let qt: Quadtree;

  const hoverMarks = Array(numSeries)
    .fill(null)
    .map(() => {
      let mark = document.createElement('div');
      mark.classList.add('bar-mark');
      mark.style.position = 'absolute';
      mark.style.background = 'rgba(255,255,255,0.2)';
      return mark;
    });

  // Needed for to calculate text positions
  let boxRectsBySeries: TimelineBoxRect[][];

  const resetBoxRectsBySeries = (count: number) => {
    boxRectsBySeries = Array(numSeries)
      .fill(null)
      .map((v) => Array(count).fill(null));
  };

  const font = `500 ${Math.round(12 * devicePixelRatio)}px ${theme.typography.fontFamily}`;
  const hovered: Array<Rect | null> = Array(numSeries).fill(null);

  const fillPaths: Map<CanvasRenderingContext2D['fillStyle'], Path2D> = new Map();
  const strokePaths: Map<CanvasRenderingContext2D['strokeStyle'], Path2D> = new Map();

  function drawBoxes(ctx: CanvasRenderingContext2D) {
    fillPaths.forEach((fillPath, fillStyle) => {
      ctx.fillStyle = fillStyle;
      ctx.fill(fillPath);
    });

    strokePaths.forEach((strokePath, strokeStyle) => {
      ctx.strokeStyle = strokeStyle;
      ctx.stroke(strokePath);
    });

    fillPaths.clear();
    strokePaths.clear();
  }

  function putBox(
    ctx: CanvasRenderingContext2D,
    rect: uPlot.RectH,
    xOff: number,
    yOff: number,
    left: number,
    top: number,
    boxWidth: number,
    boxHeight: number,
    strokeWidth: number,
    seriesIdx: number,
    valueIdx: number,
    value: any,
    discrete: boolean
  ) {
    // do not render super small boxes
    if (boxWidth < 1) {
      return;
    }

    const valueColor = getValueColor(seriesIdx + 1, value);
    const fieldConfig = getFieldConfig(seriesIdx);
    const fillColor = getFillColor(fieldConfig, valueColor);

    const boxRect = (boxRectsBySeries[seriesIdx][valueIdx] = {
      x: round(left - xOff),
      y: round(top - yOff),
      w: boxWidth,
      h: boxHeight,
      sidx: seriesIdx + 1,
      didx: valueIdx,
      // for computing text contrast in drawPoints()
      fillColor,
    });

    qt.add(boxRect);

    if (discrete) {
      let fillStyle = fillColor;
      let fillPath = fillPaths.get(fillStyle);

      if (fillPath == null) {
        fillPaths.set(fillStyle, (fillPath = new Path2D()));
      }

      rect(fillPath, left, top, boxWidth, boxHeight);

      if (strokeWidth) {
        let strokeStyle = valueColor;
        let strokePath = strokePaths.get(strokeStyle);

        if (strokePath == null) {
          strokePaths.set(strokeStyle, (strokePath = new Path2D()));
        }

        rect(
          strokePath,
          left + strokeWidth / 2,
          top + strokeWidth / 2,
          boxWidth - strokeWidth,
          boxHeight - strokeWidth
        );
      }
    } else {
      ctx.beginPath();
      rect(ctx, left, top, boxWidth, boxHeight);
      ctx.fillStyle = fillColor;
      ctx.fill();

      if (strokeWidth) {
        ctx.beginPath();
        rect(ctx, left + strokeWidth / 2, top + strokeWidth / 2, boxWidth - strokeWidth, boxHeight - strokeWidth);
        ctx.strokeStyle = valueColor;
        ctx.lineWidth = strokeWidth;
        ctx.stroke();
      }
    }
  }

  const drawPaths: Series.PathBuilder = (u, sidx, idx0, idx1) => {
    uPlot.orient(
      u,
      sidx,
      (series, dataX, dataY, scaleX, scaleY, valToPosX, valToPosY, xOff, yOff, xDim, yDim, moveTo, lineTo, rect) => {
        let strokeWidth = round((series.width || 0) * pxRatio);

        let discrete = isDiscrete(sidx);

        u.ctx.save();
        rect(u.ctx, u.bbox.left, u.bbox.top, u.bbox.width, u.bbox.height);
        u.ctx.clip();

        walk(rowHeight, sidx - 1, numSeries, yDim, (iy, y0, height) => {
          for (let ix = 0; ix < dataY.length; ix++) {
            if (dataY[ix] != null) {
              let left = Math.round(valToPosX(dataX[ix], scaleX, xDim, xOff));

              let nextIx = ix;
              while (dataY[++nextIx] === undefined && nextIx < dataY.length) {}

              // to now (not to end of chart)
              let right =
                nextIx === dataY.length
                  ? xOff + xDim + strokeWidth
                  : Math.round(valToPosX(dataX[nextIx], scaleX, xDim, xOff));

              putBox(
                u.ctx,
                rect,
                xOff,
                yOff,
                left,
                round(yOff + y0),
                right - left,
                round(height),
                strokeWidth,
                iy,
                ix,
                dataY[ix],
                discrete
              );

              ix = nextIx - 1;
            }
          }
        });

        discrete && drawBoxes(u.ctx);

        u.ctx.restore();
      }
    );

    return null;
  };

  const drawPoints: Series.Points.Show =
    formatValue == null || showValue === BarValueVisibility.Never
      ? false
      : (u, sidx, i0, i1) => {
          u.ctx.save();
          u.ctx.rect(u.bbox.left, u.bbox.top, u.bbox.width, u.bbox.height);
          u.ctx.clip();

          u.ctx.font = font;
          u.ctx.textAlign = 'left';
          u.ctx.textBaseline = 'middle';

          uPlot.orient(u, sidx, (series, dataX, dataY, scaleX, scaleY, valToPosX, valToPosY, xOff, yOff) => {
            let strokeWidth = round((series.width || 0) * pxRatio);

            let y = round(yOff + yMids[sidx - 1]);

            for (let ix = 0; ix < dataY.length; ix++) {
              if (dataY[ix] != null) {
                const boxRect = boxRectsBySeries[sidx - 1][ix];

                // Todo refine this to better know when to not render text (when values do not fit)
                if (!boxRect || (showValue === BarValueVisibility.Auto && boxRect.w < 20)) {
                  continue;
                }

                // text position w/padding
                const x = round(boxRect.x + xOff + strokeWidth / 2 + textPadding);

                // TODO: cache by fillColor to avoid setting ctx for label
                u.ctx.fillStyle = theme.colors.getContrastText(boxRect.fillColor, 3);
                u.ctx.fillText(formatValue(sidx, dataY[ix]), x, y);
              }
            }
          });

          u.ctx.restore();

          return false;
        };

  const init = (u: uPlot) => {
    let over = u.root.querySelector('.u-over')! as HTMLElement;
    over.style.overflow = 'hidden';
    hoverMarks.forEach((m) => {
      over.appendChild(m);
    });
  };

  const drawClear = (u: uPlot) => {
    qt = qt || new Quadtree(0, 0, u.bbox.width, u.bbox.height);

    qt.clear();
    resetBoxRectsBySeries(u.data[0].length);

    // force-clear the path cache to cause drawBars() to rebuild new quadtree
    u.series.forEach((s) => {
      // @ts-ignore
      s._paths = null;
    });
  };

  const setCursor = (u: uPlot) => {
    let cx = round(u.cursor!.left! * pxRatio);

    for (let i = 0; i < numSeries; i++) {
      let found: Rect | null = null;

      if (cx >= 0) {
        let cy = yMids[i];

        qt.get(cx, cy, 1, 1, (o) => {
          if (pointWithin(cx, cy, o.x, o.y, o.x + o.w, o.y + o.h)) {
            found = o;
          }
        });
      }

      let h = hoverMarks[i];

      if (found) {
        if (found !== hovered[i]) {
          hovered[i] = found;

          h.style.display = '';
          h.style.left = round(found!.x / pxRatio) + 'px';
          h.style.top = round(found!.y / pxRatio) + 'px';
          h.style.width = round(found!.w / pxRatio) + 'px';
          h.style.height = round(found!.h / pxRatio) + 'px';
        }
      } else if (hovered[i] != null) {
        h.style.display = 'none';
        hovered[i] = null;
      }
    }
  };

  // hide y crosshair & hover points
  const cursor: Partial<Cursor> = {
    y: false,
    points: { show: false },
  };

  const yMids: number[] = Array(numSeries).fill(0);
  const ySplits: number[] = Array(numSeries).fill(0);

  return {
    cursor,

    xSplits: null,

    xRange: (u: uPlot) => {
      const r = getTimeRange();
      return [r.from.valueOf(), r.to.valueOf()] as uPlot.Range.MinMax;
    },

    ySplits: (u: uPlot) => {
      walk(rowHeight, null, numSeries, u.bbox.height, (iy, y0, hgt) => {
        // vertical midpoints of each series' timeline (stored relative to .u-over)
        yMids[iy] = round(y0 + hgt / 2);
        ySplits[iy] = u.posToVal(yMids[iy] / pxRatio, FIXED_UNIT);
      });

      return ySplits;
    },

    yValues: (u: uPlot, splits: number[]) => splits.map((v, i) => label(i + 1)),
    yRange: [0, 1] as uPlot.Range.MinMax,

    // pathbuilders
    drawPaths,
    drawPoints,

    // hooks
    init,
    drawClear,
    setCursor,
  };
}

function getFillColor(fieldConfig: TimelineFieldConfig, color: string) {
  const opacityPercent = (fieldConfig.fillOpacity ?? 100) / 100;
  return tinycolor(color).setAlpha(opacityPercent).toString();
}
