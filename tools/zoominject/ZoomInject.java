import android.os.SystemClock;
import android.view.InputDevice;
import android.view.InputEvent;
import android.view.MotionEvent;

import java.lang.reflect.Method;

/**
 * Framework-level multi-touch injector.
 *
 * The guest input pipeline on BlueStacks forwards only a single pointer from
 * raw evdev writes, so a pinch written with `sendevent` collapses to a tap and
 * the game never zooms. This class builds a genuine multi-pointer MotionEvent
 * and hands it to InputManager, the same path the `input` command uses, which
 * bypasses evdev entirely.
 *
 * Run as the shell user:
 *   CLASSPATH=/data/local/tmp/zoominject.jar app_process /system/bin ZoomInject \
 *       pinch <x1s> <y1s> <x2s> <y2s> <x1e> <y1e> <x2e> <y2e> <steps> <durationMs>
 */
public final class ZoomInject {
    private static final int ACTION_DOWN = 0;
    private static final int ACTION_UP = 1;
    private static final int ACTION_MOVE = 2;
    private static final int ACTION_POINTER_DOWN = 5;
    private static final int ACTION_POINTER_UP = 6;
    private static final int INJECT_WAIT_FOR_FINISH = 2;
    private static float SIZE = 0.05f;

    public static void main(String[] args) {
        try {
            if (args.length < 1) {
                System.err.println("usage: ZoomInject pinch x1s y1s x2s y2s x1e y1e x2e y2e steps durationMs");
                System.exit(2);
            }
            String cmd = args[0];
            if ("tap".equals(cmd)) {
                float tx = Float.parseFloat(args[1]);
                float ty = Float.parseFloat(args[2]);
                Object manager0 = inputManager();
                Method inject0 = manager0.getClass().getMethod(
                        "injectInputEvent", InputEvent.class, int.class);
                long t0 = SystemClock.uptimeMillis();
                inject(inject0, manager0, frame(t0, ACTION_DOWN, new float[]{tx}, new float[]{ty}));
                sleep(60);
                inject(inject0, manager0, frame(t0, ACTION_UP, new float[]{tx}, new float[]{ty}));
                System.out.println("zoominject: tap injected");
                return;
            }
            if (!"pinch".equals(cmd)) {
                System.err.println("unknown command: " + cmd);
                System.exit(2);
            }
            float x1s = Float.parseFloat(args[1]);
            float y1s = Float.parseFloat(args[2]);
            float x2s = Float.parseFloat(args[3]);
            float y2s = Float.parseFloat(args[4]);
            float x1e = Float.parseFloat(args[5]);
            float y1e = Float.parseFloat(args[6]);
            float x2e = Float.parseFloat(args[7]);
            float y2e = Float.parseFloat(args[8]);
            int steps = Integer.parseInt(args[9]);
            int durationMs = Integer.parseInt(args[10]);
            if (args.length > 11) { SIZE = Float.parseFloat(args[11]); }
            if (steps < 1) {
                steps = 1;
            }

            Object manager = inputManager();
            Method inject = manager.getClass().getMethod(
                    "injectInputEvent", InputEvent.class, int.class);

            long downTime = SystemClock.uptimeMillis();
            long perStep = Math.max(1, durationMs / (steps + 2));

            // First finger lands.
            inject(inject, manager, frame(downTime, ACTION_DOWN, new float[]{x1s}, new float[]{y1s}));
            sleep(perStep);

            // Second finger lands; from here both pointers move.
            float[] xs = new float[]{x1s, x2s};
            float[] ys = new float[]{y1s, y2s};
            inject(inject, manager, frame(downTime, ACTION_POINTER_DOWN | (1 << 8), xs, ys));
            sleep(perStep);

            for (int i = 1; i <= steps; i++) {
                float t = (float) i / (float) steps;
                xs[0] = x1s + (x1e - x1s) * t;
                ys[0] = y1s + (y1e - y1s) * t;
                xs[1] = x2s + (x2e - x2s) * t;
                ys[1] = y2s + (y2e - y2s) * t;
                inject(inject, manager, frame(downTime, ACTION_MOVE, xs, ys));
                sleep(perStep);
            }

            // Second finger lifts, then the first.
            inject(inject, manager, frame(downTime, ACTION_POINTER_UP | (1 << 8), xs, ys));
            sleep(perStep);
            inject(inject, manager, frame(downTime, ACTION_UP, new float[]{xs[0]}, new float[]{ys[0]}));
            System.out.println("zoominject: pinch injected (steps=" + steps + ")");
        } catch (Throwable t) {
            System.err.println("zoominject: " + t);
            t.printStackTrace();
            System.exit(1);
        }
    }

    private static Object inputManager() throws Exception {
        Class<?> clazz = Class.forName("android.hardware.input.InputManager");
        Method getInstance = clazz.getMethod("getInstance");
        return getInstance.invoke(null);
    }

    private static void inject(Method inject, Object manager, MotionEvent event) throws Exception {
        inject.invoke(manager, event, INJECT_WAIT_FOR_FINISH);
        event.recycle();
    }

    private static MotionEvent frame(long downTime, int action, float[] xs, float[] ys) {
        int count = xs.length;
        MotionEvent.PointerProperties[] props = new MotionEvent.PointerProperties[count];
        MotionEvent.PointerCoords[] coords = new MotionEvent.PointerCoords[count];
        for (int i = 0; i < count; i++) {
            props[i] = new MotionEvent.PointerProperties();
            props[i].id = i;
            props[i].toolType = MotionEvent.TOOL_TYPE_FINGER;
            coords[i] = new MotionEvent.PointerCoords();
            coords[i].x = xs[i];
            coords[i].y = ys[i];
            coords[i].pressure = 1.0f;
            coords[i].size = SIZE;
        }
        long now = SystemClock.uptimeMillis();
        return MotionEvent.obtain(
                downTime, now, action, count, props, coords,
                0, 0, 1.0f, 1.0f, 0, 0,
                InputDevice.SOURCE_TOUCHSCREEN, 0);
    }

    private static void sleep(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException ignored) {
            Thread.currentThread().interrupt();
        }
    }
}
