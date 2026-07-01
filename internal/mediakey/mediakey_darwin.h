#ifndef LP10_MEDIAKEY_DARWIN_H
#define LP10_MEDIAKEY_DARWIN_H

// lp10InstallTap creates the media-key event tap and adds it to the CURRENT
// thread's run loop. Returns 1 on success, 0 if the tap could not be created
// (typically Accessibility permission not granted). Must run on the same thread
// that will call lp10RunLoop.
int lp10InstallTap(void);

// lp10RunLoop runs the current thread's run loop, blocking until lp10StopTap is
// called from another thread. Only meaningful after a successful lp10InstallTap.
void lp10RunLoop(void);

// lp10StopTap disables the tap and stops the run loop started by lp10RunLoop. It
// is safe to call from a different thread.
void lp10StopTap(void);

#endif
