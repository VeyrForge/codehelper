using UnityEngine;

namespace Example.UnityLite
{
    public class Health : MonoBehaviour
    {
        public int Current = 100;

        public void TakeDamage(int amount)
        {
            Current -= amount;
        }
    }
}
